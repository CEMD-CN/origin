package autobuild //声明包名

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	restclient "k8s.io/client-go/rest" //api操作的client sdk
	kclientcmd "k8s.io/client-go/tools/clientcmd"
	kapi "k8s.io/kubernetes/pkg/api"

	buildapi "github.com/openshift/origin/pkg/build/api"
	"github.com/openshift/origin/pkg/client"
	"github.com/openshift/origin/pkg/generate/git"
	"github.com/openshift/origin/pkg/gitserver"
)

type AutoLinkBuilds struct { //从构成中读出寓意
	Namespaces []string
	Builders   []kapi.ObjectReference
	Client     client.BuildConfigsNamespacer

	CurrentNamespace string

	PostReceiveHook string

	LinkFn func(name string) *url.URL
}

var ErrNotEnabled = fmt.Errorf("not enabled")

func NewAutoLinkBuildsFromEnvironment() (*AutoLinkBuilds, error) { //构建build结构体的方式之一：从环境中来
	config := &AutoLinkBuilds{} //声明一个空的结构体，使用的{}操作符号

	file := os.Getenv("AUTOLINK_KUBECONFIG")
	if len(file) == 0 {
		return nil, ErrNotEnabled
	}
	clientConfig, namespace, err := clientFromConfig(file)
	if err != nil {
		return nil, err
	}
	client, err := client.New(clientConfig)
	if err != nil {
		return nil, err
	}
	config.Client = client

	if value := os.Getenv("AUTOLINK_NAMESPACE"); len(value) > 0 {
		namespace = value
	}
	if len(namespace) == 0 {
		return nil, ErrNotEnabled
	}

	if value := os.Getenv("AUTOLINK_HOOK"); len(value) > 0 {
		abs, err := filepath.Abs(value)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, err
		}
		config.PostReceiveHook = abs
	}

	config.Namespaces = []string{namespace}
	config.CurrentNamespace = namespace
	return config, nil //从os包的environment对象中读取，这也是container启动是传递环境变量的价值所在
}

func clientFromConfig(path string) (*restclient.Config, string, error) {
	if path == "-" {
		cfg, err := restclient.InClusterConfig()
		if err != nil {
			return nil, "", fmt.Errorf("cluster config not available: %v", err)
		}
		return cfg, "", nil
	}
	rules := &kclientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	credentials, err := rules.Load()
	if err != nil {
		return nil, "", fmt.Errorf("the provided credentials %q could not be loaded: %v", path, err)
	}
	cfg := kclientcmd.NewDefaultClientConfig(*credentials, &kclientcmd.ConfigOverrides{})
	config, err := cfg.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("the provided credentials %q could not be used: %v", path, err)
	}
	namespace, _, _ := cfg.Namespace()
	return config, namespace, nil //使用client向kuberentes server索要namespace
}

func (a *AutoLinkBuilds) Link() (map[string]gitserver.Clone, error) {
	log.Printf("Linking build configs in namespace(s) %v to the gitserver", a.Namespaces)
	errs := []error{}
	builders := []*buildapi.BuildConfig{}
	for _, namespace := range a.Namespaces {
		list, err := a.Client.BuildConfigs(namespace).List(metav1.ListOptions{})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for i := range list.Items {
			builders = append(builders, &list.Items[i])
		}
	}
	for _, b := range a.Builders {
		if hasItem(builders, b) {
			continue
		}
		config, err := a.Client.BuildConfigs(b.Namespace).Get(b.Name, metav1.GetOptions{})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		builders = append(builders, config)
	}

	hooks := make(map[string]string)
	if len(a.PostReceiveHook) > 0 {
		hooks["post-receive"] = a.PostReceiveHook
	}

	clones := make(map[string]gitserver.Clone)
	for _, builder := range builders {
		source := builder.Spec.Source.Git
		if source == nil {
			continue
		}
		if builder.Annotations == nil {
			builder.Annotations = make(map[string]string)
		}

		// calculate the origin URL
		uri := source.URI
		if value, ok := builder.Annotations["git.openshift.io/origin-url"]; ok {
			uri = value
		}
		if len(uri) == 0 {
			continue
		}
		origin, err := git.ParseRepository(uri)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		// calculate the local repository name and self URL
		name := builder.Name
		if a.CurrentNamespace != builder.Namespace {
			name = fmt.Sprintf("%s.%s", builder.Namespace, name)
		}
		name = fmt.Sprintf("%s.git", name)
		self := a.LinkFn(name)
		if self == nil {
			errs = append(errs, fmt.Errorf("no self URL available, can't update %s", name))
			continue
		}

		// we can't clone from ourself
		if self.Host == origin.Host {
			continue
		}

		// update the existing builder
		changed := false
		if builder.Annotations["git.openshift.io/origin-url"] != origin.String() {
			builder.Annotations["git.openshift.io/origin-url"] = origin.String()
			changed = true
		}
		if source.URI != self.String() {
			source.URI = self.String()
			changed = true
		}
		if changed {
			if _, err := a.Client.BuildConfigs(builder.Namespace).Update(builder); err != nil {
				errs = append(errs, err)
				continue
			}
			log.Printf("Linked %s for repo %s as %s", builder.Name, origin.String(), self.String())
		} else {
			log.Printf("Already linked %s for repo %s as %s", builder.Name, origin.String(), self.String())
		}

		clones[name] = gitserver.Clone{
			URL:   *origin,
			Hooks: hooks,
		}
	}
	if len(clones) == 0 {
		log.Printf("No build configs found to link to the gitserver")
	}
	return clones, errors.NewAggregate(errs) 
	//从namespace中获取的build config 与git server中的关系
}

func hasItem(items []*buildapi.BuildConfig, item kapi.ObjectReference) bool {
	for _, c := range items {
		if c.Namespace == item.Namespace && c.Name == item.Name {
			return true
		}
	}
	return false
}
