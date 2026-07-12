// Command bridged is the Bridge Daemon entrypoint — the persistent control
// plane described in the CodeEditor coordinator repo's constitution
// (Principio VII) and specs/001-core-development-flows/plan.md.
package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"
)

func main() {
	port := flag.Int("port", 8443, "port to listen on")
	tlsCert := flag.String("tls-cert", "", "path to TLS certificate")
	tlsKey := flag.String("tls-key", "", "path to TLS private key")
	flag.Parse()

	if *tlsCert == "" || *tlsKey == "" {
		log.Fatal("bridged: --tls-cert and --tls-key are required")
	}

	log.Printf("bridged: listening on :%d", *port)
	log.Fatal(http.ListenAndServeTLS(":"+strconv.Itoa(*port), *tlsCert, *tlsKey, nil))
}
