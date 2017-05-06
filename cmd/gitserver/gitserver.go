package main
//使用main包的main函数作为入口，启动程序的层层执行
import (
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"k8s.io/kubernetes/pkg/util/logs"

	"github.com/openshift/origin/pkg/cmd/infra/gitserver"
	"github.com/openshift/origin/pkg/cmd/util/serviceability"

	// install all APIs
	_ "github.com/openshift/origin/pkg/api/install" //安装包用来实现安装
	_ "k8s.io/kubernetes/pkg/api/install"
	_ "k8s.io/kubernetes/pkg/apis/extensions/install"
)

func main() {
	logs.InitLogs()
	defer logs.FlushLogs()
	defer serviceability.BehaviorOnPanic(os.Getenv("OPENSHIFT_ON_PANIC"))()
	defer serviceability.Profile(os.Getenv("OPENSHIFT_PROFILE")).Stop()

	rand.Seed(time.Now().UTC().UnixNano())
	if len(os.Getenv("GOMAXPROCS")) == 0 {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	basename := filepath.Base(os.Args[0])
	command := gitserver.CommandFor(basename)//包的位置在pkg／cmd目录中；
	if err := command.Execute(); err != nil {//执行execute函数
		os.Exit(1)//退出程序，给出退出状态
	}
}
