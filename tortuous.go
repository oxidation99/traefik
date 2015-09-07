package main

import (
	"github.com/gorilla/mux"
	"github.com/mailgun/oxy/forward"
	"github.com/mailgun/oxy/roundrobin"
	"github.com/tylerb/graceful"
	"github.com/unrolled/render"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"
	"log"
	"github.com/BurntSushi/toml"
)

type FileConfiguration struct {
	Docker *DockerProvider
	File *FileProvider
}

var srv *graceful.Server
var configurationRouter *mux.Router
var renderer = render.New()
var currentConfiguration = new(Configuration)
var configurationChan = make(chan *Configuration)
var providers = []Provider{}

func main() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	systemRouter := mux.NewRouter()
	systemRouter.Methods("POST").Path("/reload").HandlerFunc(ReloadConfigHandler)
	systemRouter.Methods("GET").Path("/").HandlerFunc(GetConfigHandler)
	go http.ListenAndServe(":8000", systemRouter)

	go func() {
		for {
			configuration := <-configurationChan
			log.Println("Configuration receveived", configuration)
			if configuration == nil {
				log.Println("Skipping empty configuration")
			} else if(reflect.DeepEqual(currentConfiguration, configuration)){
				log.Println("Skipping same configuration")
			} else{
				currentConfiguration = configuration
				configurationRouter = LoadConfig(configuration)
				srv.Stop(10 * time.Second)
				time.Sleep(3 * time.Second)
			}
		}
	}()

	configuration := LoadFileConfig()
	log.Println("Configuration loaded", configuration)
	if(configuration.Docker != nil){
		providers = append(providers, configuration.Docker)
	}

	if(configuration.File != nil){
		providers = append(providers, configuration.File)
	}

	for _, provider := range providers {
		go func() {
			provider.Provide(configurationChan)
		}()
	}

	goAway := false
	go func() {
		sig := <-sigs
		log.Println("I have to go...", sig)
		goAway = true
		srv.Stop(10 * time.Second)
	}()

	for {
		if goAway {
			break
		}
		srv = &graceful.Server{
			Timeout:          10 * time.Second,
			NoSignalHandling: true,

			ConnState: func(conn net.Conn, state http.ConnState) {
				// conn has a new state
			},

			Server: &http.Server{
				Addr:    ":8001",
				Handler: configurationRouter,
			},
		}

		go srv.ListenAndServe()
		log.Println("Started")
		<-srv.StopChan()
		log.Println("Stopped")
	}
}

func LoadConfig(configuration *Configuration) *mux.Router {
	router := mux.NewRouter()
	for routeName, route := range configuration.Routes {
		log.Println("Creating route", routeName)
		fwd, _ := forward.New()
		newRoutes := []*mux.Route{}
		for ruleName, rule := range route.Rules {
			log.Println("Creating rule", ruleName)
			newRouteReflect := Invoke(router.NewRoute(), rule.Category, rule.Value)
			newRoute := newRouteReflect[0].Interface().(*mux.Route)
			newRoutes = append(newRoutes, newRoute)
		}
		for _, backendName := range route.Backends {
			log.Println("Creating backend", backendName)
			lb, _ := roundrobin.New(fwd)
			rb, _ := roundrobin.NewRebalancer(lb)
			for serverName, server := range configuration.Backends[backendName].Servers {
				log.Println("Creating server", serverName)
				url, _ := url.Parse(server.Url)
				rb.UpsertServer(url)
			}
			for _, route := range newRoutes {
				route.Handler(lb)
			}
		}
	}
	return router
}

func DeployService() {
	configurationRouter = LoadConfig(currentConfiguration)
}

func ReloadConfigHandler(rw http.ResponseWriter, r *http.Request) {
	DeployService()
	srv.Stop(10 * time.Second)
	renderer.JSON(rw, http.StatusOK, map[string]interface{}{"status": "reloaded"})
}

func RestartHandler(rw http.ResponseWriter, r *http.Request) {
	renderer.JSON(rw, http.StatusOK, map[string]interface{}{"status": "restarted"})
}

func GetConfigHandler(rw http.ResponseWriter, r *http.Request) {
	renderer.JSON(rw, http.StatusOK, currentConfiguration)
}

func Invoke(any interface{}, name string, args ...interface{}) []reflect.Value {
	inputs := make([]reflect.Value, len(args))
	for i, _ := range args {
		inputs[i] = reflect.ValueOf(args[i])
	}
	return reflect.ValueOf(any).MethodByName(name).Call(inputs)
}

func LoadFileConfig() *FileConfiguration  {
	configuration := new(FileConfiguration)
	if _, err := toml.DecodeFile("tortuous.toml", configuration); err != nil {
		log.Fatal("Error reading file:", err)
	}
	return configuration
}