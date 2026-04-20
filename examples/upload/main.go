// examples/upload — stream a local file to a remote filesystem.
//
// Usage:
//
//	go run ./examples/upload \
//	    -type 2 \
//	    -cfg host=x,user=y,pass=z,insecure_host_key=true \
//	    -src ./localfile \
//	    -dst /remote/path.bin
//
// Demonstrates the streaming CreateFile contract: the driver pulls
// bytes from the local reader on demand and the local file never has
// to fit in RAM all at once.
package main

import (
	"flag"
	"io"
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
	driverType := flag.Int("type", 0, "driver type id")
	cfgStr := flag.String("cfg", "", "k=v,k=v config for Mount")
	src := flag.String("src", "", "local file to upload")
	dst := flag.String("dst", "", "remote destination path")
	flag.Parse()

	if *driverType == 0 || *src == "" || *dst == "" {
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

	f, err := os.Open(*src)
	if err != nil {
		log.Fatalf("open src: %v", err)
	}
	defer f.Close()

	w, err := drv.CreateFile(1, *dst)
	if err != nil {
		log.Fatalf("create: %v", err)
	}

	n, err := io.Copy(w, f)
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		log.Fatalf("copy: %v", err)
	}
	log.Printf("uploaded %d bytes to %s", n, *dst)
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
