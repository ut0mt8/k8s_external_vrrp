package main

import (
	"context"
	"fmt"
	"github.com/ericchiang/k8s"
	corev1 "github.com/ericchiang/k8s/apis/core/v1"
	"github.com/ghodss/yaml"
	"github.com/namsral/flag"
	"github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"os/exec"
	"reflect"
	"text/template"
	"time"
)

type Config struct {
	kubeConfig   string
	tmplFile     string
	configFile   string
	reloadScript string
	syncPeriod   int
	debug        bool
}

type Service struct {
	Name      string
	Namespace string
	IP        string
}

var config Config
var log = logrus.New()

func loadClient(kubeconfigPath string) (*k8s.Client, error) {

	data, err := ioutil.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %v", err)
	}

	var cfg k8s.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal kubeconfig: %v", err)
	}

	return k8s.NewClient(&cfg)
}

func getServices(client *k8s.Client) (services []Service, err error) {

	var svcs corev1.ServiceList
	err = client.List(context.Background(), k8s.AllNamespaces, &svcs)

	if err != nil {
		return nil, fmt.Errorf("Cannot list services: %v", err)
	}

	for _, s := range svcs.Items {

		log.Debugf("Service Candidate : %v:%+v type=%+v", *s.Metadata.Namespace, *s.Metadata.Name, *s.Spec.Type)

		if *s.Spec.Type != "LoadBalancer" {
			log.Debugf(" - Dropped candidate : %+v, not loadbalancer type", *s.Metadata.Name)
			continue
		}

		if *s.Spec.LoadBalancerIP == "" {
			log.Debugf(" - Dropped candidate : %+v, no loadbalancer IP", *s.Metadata.Name)
			continue
		}

		cService := Service{
			Name:      *s.Metadata.Name,
			Namespace: *s.Metadata.Namespace,
			IP:        *s.Spec.LoadBalancerIP,
		}

		services = append(services, cService)

		log.Debugf("Candidate OK : %+v", cService)
	}

	return services, nil
}

func configureServices(services []Service, tmplFile string, configFile string) {

	for n, service := range services {
		log.Infof("-+= Service #%v", n)
		log.Infof(" |--== Name : %v", service.Name)
		log.Infof(" |--== Namespace : %v", service.Namespace)
		log.Infof(" `--== IP : %v", service.IP)
	}

	t, err := template.ParseFiles(tmplFile)
	if err != nil {
		log.Errorf("Failed to load template file: %v", err)
		return
	}

	w, err := os.Create(configFile)
	if err != nil {
		log.Errorf("Failed to open config file: %v", err)
		return
	}

	conf := make(map[string]interface{})
	conf["services"] = services

	err = t.Execute(w, conf)
	if err != nil {
		log.Errorf("Failed to write config file: %v", err)
		return
	} else {
		log.Infof("Write config file: %v", configFile)
	}

	log.Infof("Ready to reload proxy")

	out, err := exec.Command(config.reloadScript).CombinedOutput()
	if err != nil {
		log.Errorf("Error reloading proxy: %v\n%s", err, out)
	} else {
		log.Infof("Reload script succeed:\n%s", out)
	}

	return
}

func init() {

	flag.StringVar(&config.kubeConfig, "kubeConfig", os.Getenv("HOME")+"/.kube/config", "kubeconfig file to load")
	flag.StringVar(&config.tmplFile, "tmplFile", "config.tmpl", "Template file to load")
	flag.StringVar(&config.configFile, "configFile", "config.conf", "Configuration file to write")
	flag.StringVar(&config.reloadScript, "reloadScript", "./reload.sh", "Reload script to launch")
	flag.IntVar(&config.syncPeriod, "syncPeriod", 10, "Period between update")
	flag.BoolVar(&config.debug, "debug", false, "Enable debug messages")

	log.Formatter = new(logrus.TextFormatter)
	log.Level = logrus.InfoLevel
}

func main() {

	flag.Parse()
	if config.debug {
		log.SetLevel(logrus.DebugLevel)
	}

	client, err := loadClient(config.kubeConfig)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	log.Infof("Initial GetServices fired")
	currentServices, err := getServices(client)
	if err != nil {
		log.Fatalf("Failed initial GetServices: %v", err)
	}
	configureServices(currentServices, config.tmplFile, config.configFile)

	for t := range time.NewTicker(time.Duration(config.syncPeriod) * time.Second).C {

		log.Debugf("GetServices fired at %+v", t)
		newServices, err := getServices(client)
		if err != nil {
			log.Errorf("Failed GetServices: %v", err)
		}

		if !reflect.DeepEqual(newServices, currentServices) {
			log.Infof("Services have changed, reload fired")
			currentServices = newServices
			configureServices(currentServices, config.tmplFile, config.configFile)
		}
	}
}
