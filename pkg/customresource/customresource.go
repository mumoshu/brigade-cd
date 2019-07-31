package customresource

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/brigadecore/brigade/pkg/brigade"
	"github.com/brigadecore/brigade/pkg/storage"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// Payload represents the data sent as the payload of an event.
type Payload struct {
	Type         string      `json:"type"`
	Token        string      `json:"token"`
	TokenExpires time.Time   `json:"tokenExpires"`
	Body         interface{} `json:"body"`
	AppID        int         `json:"-"`
	InstID       int         `json:"-"`
	Commit       string      `json:"commit"`
	Branch       string      `json:"branch"`
}

type Object struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   Spec   `json:"spec"`
	Status Status `json:"status"`
}

type Spec struct {
	Raw map[string]interface{} `json:",inline"`
}

type Status struct {
	Phase string `json:"phase"`
}

type State struct {
	Object Object `json:"object"`
}

type Handler struct {
	store                            storage.Store
	brigadeProject                   string
	eventTypeUpdate, eventTypeDelete string
	branch                           string

	kubeclient client.Client

	groupVersionKind schema.GroupVersionKind
}

func (h *Handler) HandleState(ss *state.State) error {
	s := State{}

	err := state.Unpack(ss, &s)
	if err != nil {
		return err
	}

	o := s.Object

	// Here we build/populate Brigade's webhook.Payload object
	//
	// Note we also add commit and branch data here, as neither is
	// included in the github.IssueCommentEvent (here res.Body)
	// The check run utility that requests check runs requires these values
	// and does not have access to he brigade.Revision object above.
	res := &Payload{
		//AppID:        appID,
		//InstID:       int(instID),
		Type: strings.ToLower(o.Kind),
		//Token:        tok,
		//TokenExpires: timeout,
		//Commit:       rev.Commit,
		Branch: h.branch,
	}

	body, err := json.Marshal(o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal object into body: %s\n", err)
		return err
	}
	res.Body = body

	payload, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintf(os.Stderr, "JSON encoding error: %v\n", err)
		return err
	}

	proj, err := h.store.GetProject(h.brigadeProject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Project %q not found. No secret loaded. %s\n", h.brigadeProject, err)
		return err
	}

	obj := &unstructured.Unstructured{}
	//instanceList := &unstructured.UnstructuredList{}
	//instanceList.SetGroupVersionKind(h.groupVersionKind)
	namespacedName := types.NamespacedName{Name: o.Name, Namespace: o.Namespace}
	err = h.kubeclient.Get(context.TODO(), namespacedName, obj)

	var eventType string
	if err != nil {
		if errors.IsNotFound(err) {
			eventType = h.eventTypeDelete
		} else {
			return err
		}
	} else {
		eventType = h.eventTypeUpdate
	}

	if err := h.build(eventType, payload, proj); err != nil {
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

func (h *Handler) build(eventType string, payload []byte, proj *brigade.Project) error {
	b := &brigade.Build{
		ProjectID: proj.ID,
		Type:      eventType,
		Provider:  "brigade-cd",
		//Revision:  &rev,
		Payload: payload,
	}
	return h.store.CreateBuild(b)
}

type Mapping struct {
	Group, Version, Kind string
	BrigadeProject       string
}

type controller struct {
	keys []Mapping
	s    storage.Store
}

func New(s storage.Store, mappings []Mapping) *controller {
	return &controller{
		s:    s,
		keys: mappings,
	}
}

func (ct *controller) Run() error {
	logf.SetLogger(logf.ZapLogger(false))

	configs := []*config.ResourceConfig{}
	handlers := []*Handler{}
	for _, k := range ct.keys {
		groupVersionKind := schema.GroupVersionKind{
			Group:   k.Group,
			Version: k.Version,
			Kind:    k.Kind,
		}
		lkind := strings.ToLower(k.Kind)
		handler := &Handler{
			store:           ct.s,
			brigadeProject:  k.BrigadeProject,
			eventTypeDelete: fmt.Sprintf("%s:delete", lkind),
			eventTypeUpdate: fmt.Sprintf("%s:update", lkind),
			branch:          "master",
			groupVersionKind: groupVersionKind,
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

	kc, err := kconfig.GetConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load kubeconfig: %s\n", err)
		return err
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
