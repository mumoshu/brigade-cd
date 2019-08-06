package customresource

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/brigadecore/brigade/pkg/brigade"
	"github.com/brigadecore/brigade/pkg/storage"
	"github.com/mumoshu/brigade-cd/pkg/webhook"
	"k8s.io/client-go/rest"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	_ "k8s.io/client-go/plugin/pkg/client/auth/azure"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	kconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"

	"github.com/summerwind/whitebox-controller/config"
	"github.com/summerwind/whitebox-controller/manager"
	"github.com/summerwind/whitebox-controller/reconciler/state"
)

type Object struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   map[string]interface{} `json:"spec"`
	Status Status                 `json:"status"`
}

type Status struct {
	Phase string `json:"phase"`
}

type State struct {
	Object Object `json:"object"`
}

type Handler struct {
	store                  storage.Store
	brigadeProject         string
	eventTypeActionApply   string
	eventTypeActionDestroy string
	eventTypeActionPlan    string
	defaultBranch          string

	kubeclient client.Client

	groupVersionKind schema.GroupVersionKind

	// key is the x509 certificate key as ASCII-armored (PEM) data
	key []byte

	appID int
}

func (h *Handler) HandleState(ss *state.State) error {
	s := State{}

	err := state.Unpack(ss, &s)
	if err != nil {
		return err
	}

	o := s.Object

	// Here we build/populate Brigade's Payload object
	//
	// Note we also add commit and defaultBranch data here, as neither is
	// included in the github.IssueCommentEvent (here payload.Body)
	// The check run utility that requests check runs requires these values
	// and does not have access to he brigade.Revision object above.
	eventType := strings.ToLower(o.Kind)
	payload := &Payload{
		Type: eventType,
		//Token:        tok,
		//TokenExpires: timeout,
		//Commit:       rev.Commit,
		//Branch: h.defaultBranch,
	}

	instIDStr := o.Annotations["cd.brigade.sh/github-app-inst-id"]
	approvedStr := o.Annotations["cd.brigade.sh/approved"]
	dryRunStr := o.Annotations["cd.brigade.sh/dry-run"]
	gitRepo := o.Annotations["cd.brigade.sh/git-repo"]
	gitCommitId := o.Annotations["cd.brigade.sh/git-commit"]
	gitBranch := o.Annotations["cd.brigade.sh/git-branch"]
	pullIdStr := o.Annotations["cd.brigade.sh/github-pull-id"]

	{
		tmp := strings.Split(gitRepo, "/")
		owner := tmp[0]
		repo := tmp[1]
		payload.Owner = owner
		payload.Repo = repo
		payload.Pull = pullIdStr
	}

	if gitCommitId != "" {
		payload.Commit = gitCommitId
	} else {
		payload.Branch = h.defaultBranch
	}

	if gitBranch != "" {
		payload.Branch = gitBranch
	}

	appID := h.appID
	payload.AppID = appID

	var instID int
	if len(instIDStr) > 0 {
		instID, err = strconv.Atoi(instIDStr)
		if err != nil {
			return fmt.Errorf("failed converting %q: %v", instIDStr, err)
		}
		payload.InstID = instID
	}

	projName := h.brigadeProject

	proj, err := h.store.GetProject(projName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Project %q not found. No secret loaded. %s\n", projName, err)
		return err
	}

	if instID > 0 && appID > 0 {
		tok, timeout, err := h.installationToken(int(appID), int(instID), proj.Github)
		if err != nil {
			return fmt.Errorf("Failed to negotiate a token: %s", err)
		}
		payload.Token = tok
		payload.TokenExpires = timeout
	}

	// Check if it can be marshalled into JSON
	if _, err := json.Marshal(o); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal object into body: %s\n", err)
		return err
	}

	pullUrl := fmt.Sprintf(`https://api.github.com/repos/%s/pulls/%s`, projName, pullIdStr)
	payload.PullURL = pullUrl

	// Save the object as-is for use from within brigade.js
	payload.Body = o

	//obj := &unstructured.Unstructured{}
	//obj.SetGroupVersionKind(h.groupVersionKind)
	////instanceList := &unstructured.UnstructuredList{}
	////instanceList.SetGroupVersionKind(h.groupVersionKind)
	//namespacedName := types.NamespacedName{Name: o.Name, Namespace: o.Namespace}
	//err = h.kubeclient.Get(context.TODO(), namespacedName, obj)
	//
	//var eventTypeAction string
	//if err != nil {
	//	if errors.IsNotFound(err) || {
	//		eventTypeAction = h.eventTypeActionDestroy
	//	} else {
	//		return err
	//	}
	//} else {
	//	eventTypeAction = h.eventTypeActionApply
	//}
	var eventTypeAction string
	if o.ObjectMeta.DeletionTimestamp != nil {
		eventTypeAction = h.eventTypeActionDestroy
	} else if approvedStr == "" || approvedStr == "true" || approvedStr == "yes" && (dryRunStr == "" || dryRunStr == "no" || dryRunStr == "false") {
		eventTypeAction = h.eventTypeActionApply
	} else {
		eventTypeAction = h.eventTypeActionPlan
	}

	if err := h.build(eventTypeAction, payload, proj); err != nil {
		return err
	}

	if o.Status.Phase != "completed" {
		o.Status.Phase = "completed"
	}

	err = state.Pack(&s, ss)
	if err != nil {
		return err
	}

	return nil
}

func (h *Handler) build(eventAction string, payload *Payload, proj *brigade.Project) error {
	payloadJsonBytes, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "JSON encoding error: %v\n", err)
		return err
	}

	b := &brigade.Build{
		ProjectID: proj.ID,
		Type:      eventAction,
		Provider:  "brigade-cd",
		Revision: &brigade.Revision{
			Commit: payload.Commit,
			Ref:    fmt.Sprintf("refs/heads/%s", payload.Branch),
		},
		Payload: payloadJsonBytes,
	}
	fmt.Fprintf(os.Stderr, "Emitting event %q, payload %s\n", eventAction, payload)
	return h.store.CreateBuild(b)
}

type Mapping struct {
	Group, Version, Kind string
	BrigadeProject       string
}

type controller struct {
	mappings []Mapping
	s        storage.Store
	kc       *rest.Config
	// key is the x509 certificate key as ASCII-armored (PEM) data
	key   []byte
	appID int
}

func New(s storage.Store, appID int, key []byte, kc *rest.Config, mappings []Mapping) *controller {
	return &controller{
		s:        s,
		mappings: mappings,
		kc:       kc,
		key:      key,
		appID:    appID,
	}
}

func (ct *controller) Run() error {
	logf.SetLogger(logf.ZapLogger(false))

	configs := []*config.ResourceConfig{}
	handlers := []*Handler{}
	for _, k := range ct.mappings {
		groupVersionKind := schema.GroupVersionKind{
			Group:   k.Group,
			Version: k.Version,
			Kind:    k.Kind,
		}
		lkind := strings.ToLower(k.Kind)
		handler := &Handler{
			store:                  ct.s,
			brigadeProject:         k.BrigadeProject,
			eventTypeActionDestroy: fmt.Sprintf("%s:destroy", lkind),
			eventTypeActionApply:   fmt.Sprintf("%s:apply", lkind),
			eventTypeActionPlan:    fmt.Sprintf("%s:plan", lkind),
			defaultBranch:          "master",
			groupVersionKind:       groupVersionKind,
			key:                    ct.key,
			appID:                  ct.appID,
		}
		cfg := &config.ResourceConfig{
			GroupVersionKind: groupVersionKind,
			Reconciler: &config.ReconcilerConfig{
				HandlerConfig: config.HandlerConfig{
					StateHandler: handler,
				},
			},
		}

		configs = append(configs, cfg)
		handlers = append(handlers, handler)
	}

	c := &config.Config{
		Resources: configs,
	}

	var kc *rest.Config
	if ct.kc != nil {
		kc = ct.kc
	} else {
		var err error
		kc, err = kconfig.GetConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load kubeconfig: %s\n", err)
			return err
		}
	}

	mgr, err := manager.New(c, kc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create controller manager: %s\n", err)
		return err
	}

	for i, _ := range configs {
		handlers[i].kubeclient = mgr.GetClient()
	}

	go func() {
		err = mgr.Start(signals.SetupSignalHandler())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start controller manager: %s\n", err)
			panic(err)
		}
	}()

	return nil
}

func (s *Handler) installationToken(appID, installationID int, cfg brigade.Github) (string, time.Time, error) {
	aidStr := strconv.Itoa(appID)
	// We need to perform auth here, and then inject the token into the
	// body so that the app can use it.
	tok, err := webhook.JWT(aidStr, s.key)
	if err != nil {
		return "", time.Time{}, err
	}

	ghc, err := webhook.GhClient(brigade.Github{
		Token:     tok,
		BaseURL:   cfg.BaseURL,
		UploadURL: cfg.UploadURL,
	})

	if err != nil {
		return "", time.Time{}, err
	}

	ctx := context.Background()
	itok, _, err := ghc.Apps.CreateInstallationToken(ctx, int64(installationID))
	if err != nil {
		return "", time.Time{}, err
	}
	return itok.GetToken(), itok.GetExpiresAt(), nil
}
