// examples/list — list one directory on a remote filesystem.
//
// Usage:
//
//	go run ./examples/list \
//	    -type 1 \
//	    -cfg host=ftp.example.com,user=anon,pass= \
//	    -path /
//
// -type is the driver type id (see ROADMAP.md). -cfg is a comma-separated
// list of key=value pairs handed straight to the driver's Mount().
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/christhomas/go-networkfs/pkg/api"

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
	driverType := flag.Int("type", 0, "driver type id (1=FTP, 2=SFTP, 3=SMB, 4=Dropbox, 5=WebDAV, 6=GDrive, 7=S3, 8=OneDrive)")
	cfgStr := flag.String("cfg", "", "comma-separated key=value config pairs")
	path := flag.String("path", "/", "remote path to list")
	flag.Parse()

	if *driverType == 0 {
		flag.Usage()
		os.Exit(2)
	}

	drv, ok := api.GetDriver(*driverType)
	if !ok {
		log.Fatalf("no driver registered for type %d", *driverType)
	}

	cfg := parseCSV(*cfgStr)

	if err := drv.Mount(1, cfg); err != nil {
		log.Fatalf("mount: %v", err)
	}
	defer drv.Unmount(1)

	entries, err := drv.ListDir(1, *path)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	for _, e := range entries {
		kind := "file"
		if e.IsDir {
			kind = "dir "
		}
		fmt.Printf("%s  %10d  %s\n", kind, e.Size, e.Name)
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
