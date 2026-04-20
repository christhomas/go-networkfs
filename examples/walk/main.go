// examples/walk — recursive directory traversal, printed as paths.
//
// Shows how to use fsutil.Walk on top of the Driver interface. The
// helper works with any registered driver; this program is
// driver-agnostic apart from the blank imports.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/christhomas/go-networkfs/pkg/api"
	"github.com/christhomas/go-networkfs/pkg/fsutil"

	_ "github.com/christhomas/go-networkfs/dropbox"
	_ "github.com/christhomas/go-networkfs/ftp"
	_ "github.com/christhomas/go-networkfs/gdrive"
	_ "github.com/christhomas/go-networkfs/onedrive"
	_ "github.com/christhomas/go-networkfs/s3"
	_ "github.com/christhomas/go-networkfs/sftp"
	_ "github.com/christhomas/go-networkfs/smb"
	_ "github.com/christhomas/go-networkfs/webdav"
)

func main() {
	driverType := flag.Int("type", 0, "driver type id")
	cfgStr := flag.String("cfg", "", "k=v,k=v config for Mount")
	root := flag.String("root", "/", "root path to walk")
	flag.Parse()

	if *driverType == 0 {
		flag.Usage()
		os.Exit(2)
	}

	drv, ok := api.GetDriver(*driverType)
	if !ok {
		log.Fatalf("no driver for type %d", *driverType)
	}

	if err := drv.Mount(1, parseCSV(*cfgStr)); err != nil {
		log.Fatalf("mount: %v", err)
	}
	defer drv.Unmount(1)

	err := fsutil.Walk(drv, 1, *root, func(path string, info api.FileInfo, err error) error {
		if err != nil {
			log.Printf("walk error at %s: %v", path, err)
			return nil // keep walking peers
		}
		if info.IsDir {
			fmt.Println(path + "/")
		} else {
			fmt.Println(path)
		}
		return nil
	})
	if err != nil {
		log.Fatalf("walk: %v", err)
	}
}

func parseCSV(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	for _, kv := range strings.Split(s, ",") {
		i := strings.Index(kv, "=")
		if i < 0 {
			continue
		}
		out[strings.TrimSpace(kv[:i])] = strings.TrimSpace(kv[i+1:])
	}
	return out
}
