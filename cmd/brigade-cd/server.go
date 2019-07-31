package main

import (
	"flag"
	"fmt"
	"github.com/mumoshu/brigade-cd/pkg/customresource"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"gopkg.in/gin-gonic/gin.v1"
	v1 "k8s.io/api/core/v1"

	"github.com/brigadecore/brigade/pkg/storage/kube"

	"github.com/mumoshu/brigade-cd/pkg/webhook"
)

var (
	kubeconfig     string
	master         string
	namespace      string
	gatewayPort    string
	keyFile        string
	allowedAuthors authors
	emittedEvents  events
)

// defaultAllowedAuthors is the default set of authors allowed to PR
// https://developer.github.com/v4/reference/enum/commentauthorassociation/
var defaultAllowedAuthors = []string{"COLLABORATOR", "OWNER", "MEMBER"}

// defaultEmittedEvents is the default set of events to be emitted by the gateway
var defaultEmittedEvents = []string{"*"}

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&master, "master", "", "master url")
	flag.StringVar(&namespace, "namespace", defaultNamespace(), "kubernetes namespace")
	flag.StringVar(&gatewayPort, "gateway-port", defaultGatewayPort(), "TCP port to use for brigade-cd")
	flag.StringVar(&keyFile, "key-file", "/etc/brigade-cd/key.pem", "path to x509 key for GitHub app")
	flag.Var(&allowedAuthors, "authors", "allowed author associations, separated by commas (COLLABORATOR, CONTRIBUTOR, FIRST_TIMER, FIRST_TIME_CONTRIBUTOR, MEMBER, OWNER, NONE)")
	flag.Var(&emittedEvents, "events", "events to be emitted and passed to worker, separated by commas (defaults to `*`, which matches everything)")
}

func main() {
	flag.Parse()

	if len(keyFile) == 0 {
		log.Fatal("Key file is required")
		os.Exit(1)
	}

	key, err := ioutil.ReadFile(keyFile)
	if err != nil {
		log.Fatalf("could not load key from %q: %s", keyFile, err)
		os.Exit(1)
	}

	if len(allowedAuthors) == 0 {
		if aa, ok := os.LookupEnv("BRIGADE_AUTHORS"); ok {
			(&allowedAuthors).Set(aa)
		} else {
			allowedAuthors = defaultAllowedAuthors
		}
	}

	if len(allowedAuthors) > 0 {
		log.Printf("Forked PRs will be built for roles %s", strings.Join(allowedAuthors, " | "))
	}

	if len(emittedEvents) == 0 {
		if ee, ok := os.LookupEnv("BRIGADE_EVENTS"); ok {
			(&emittedEvents).Set(ee)
		} else {
			emittedEvents = defaultEmittedEvents
		}
	}

	envOrInt := func(env string, defaultVal int) int {
		aa, ok := os.LookupEnv(env)
		if !ok {
			return defaultVal
		}

		realVal, err := strconv.Atoi(aa)
		if err != nil {
			return defaultVal
		}
		return realVal
	}

	ghOpts := webhook.GithubOpts{
		AppID:               envOrInt("APP_ID", 0),
		DefaultSharedSecret: os.Getenv("DEFAULT_SHARED_SECRET"),
		EmittedEvents:       emittedEvents,
	}

	clientset, err := kube.GetClient(master, kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	store := kube.New(clientset, namespace)

	router := gin.New()
	router.Use(gin.Recovery())

	events := router.Group("/events")
	{
		events.Use(gin.Logger())
		events.POST("/github", webhook.NewGithubHookHandler(store, allowedAuthors, key, ghOpts))
		events.POST("/github/:app/:inst", webhook.NewGithubHookHandler(store, allowedAuthors, key, ghOpts))
	}

	router.GET("/healthz", healthz)

	keys := []customresource.Mapping{}
	c := customresource.New(store, keys)
	if err := c.Run(); err != nil {
		log.Fatal(err)
	}

	formattedGatewayPort := fmt.Sprintf(":%v", gatewayPort)
	if err := router.Run(formattedGatewayPort); err != nil {
		log.Fatal(err)
	}
}

func defaultNamespace() string {
	if ns, ok := os.LookupEnv("BRIGADE_NAMESPACE"); ok {
		return ns
	}
	return v1.NamespaceDefault
}

func defaultGatewayPort() string {
	if port, ok := os.LookupEnv("BRIGADE_GATEWAY_PORT"); ok {
		return port
	}
	return "7746"
}

func healthz(c *gin.Context) {
	c.String(http.StatusOK, http.StatusText(http.StatusOK))
}

type authors []string

func (a *authors) Set(value string) error {
	for _, aa := range strings.Split(value, ",") {
		*a = append(*a, strings.ToUpper(aa))
	}
	return nil
}

func (a *authors) String() string {
	return strings.Join(*a, ",")
}

type events []string

func (a *events) Set(value string) error {
	for _, aa := range strings.Split(value, ",") {
		*a = append(*a, strings.ToUpper(aa))
	}
	return nil
}

func (a *events) String() string {
	return strings.Join(*a, ",")
}
