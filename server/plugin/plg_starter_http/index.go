package plg_starter_http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	. "github.com/mickael-kerjean/filestash/server/common"

	"github.com/gorilla/mux"
)

func init() {
	Hooks.Register.Starter(Start)
}

func Start(ctx context.Context, r *mux.Router) {
	port := Config.Get("general.port").Int()

	caBundle := loadCABundle()
	configureTrustStore(caBundle)

	certFile := envOrDefault("TLS_CERT", "/etc/ssl/tls.crt")
	keyFile := envOrDefault("TLS_KEY", "/etc/ssl/tls.key")
	if fileExists(certFile) && fileExists(keyFile) {
		startHTTPS(ctx, r, port, certFile, keyFile, caBundle)
	} else {
		startHTTP(ctx, r, port)
	}
}

func startHTTP(ctx context.Context, r *mux.Router, port int) {
	Log.Info("[http] starting ...")
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: r,
	}
	go func() {
		ensureAppHasBooted(
			fmt.Sprintf("http://127.0.0.1:%d%s", port, WithBase("/about")),
			fmt.Sprintf("[http] listening on :%d", port),
		)
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		Log.Error("error: %v", err)
	}
}

func startHTTPS(ctx context.Context, r *mux.Router, port int, certFile, keyFile string, caBundle *x509.CertPool) {
	Log.Info("[https] starting with cert=%s key=%s", certFile, keyFile)
	tlsConfig := DefaultTLSConfig.Clone()
	tlsConfig.GetCertificate = newCertReloader(certFile, keyFile)
	srv := &http.Server{
		Addr:      fmt.Sprintf(":%d", port),
		Handler:   r,
		TLSConfig: tlsConfig,
		ErrorLog:  NewNilLogger(),
	}
	go func() {
		bootClient := &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		ensureAppHasBootedWith(
			bootClient,
			fmt.Sprintf("https://127.0.0.1:%d%s", port, WithBase("/about")),
			fmt.Sprintf("[https] listening on :%d", port),
		)
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		Log.Error("[https] error: %v", err)
	}
}

// newCertReloader returns a GetCertificate function that re-reads the
// cert and key from disk on each TLS handshake, so cert-manager
// rotations are picked up without restart. The result is cached and
// only reloaded when the file modtime changes.
func newCertReloader(certFile, keyFile string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	var (
		mu      sync.RWMutex
		cached  *tls.Certificate
		modTime time.Time
	)
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		info, err := os.Stat(certFile)
		if err != nil {
			return nil, err
		}
		mu.RLock()
		if cached != nil && info.ModTime().Equal(modTime) {
			defer mu.RUnlock()
			return cached, nil
		}
		mu.RUnlock()

		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		cached = &cert
		modTime = info.ModTime()
		mu.Unlock()
		Log.Info("[https] loaded certificate from %s", certFile)
		return &cert, nil
	}
}

// loadCABundle reads the CA bundle file specified by the CA_BUNDLE env
// var. Returns the system cert pool augmented with the bundle, or nil
// if CA_BUNDLE is not set.
func loadCABundle() *x509.CertPool {
	bundlePath := os.Getenv("CA_BUNDLE")
	if bundlePath == "" {
		return nil
	}
	pem, err := os.ReadFile(bundlePath)
	if err != nil {
		Log.Warning("[tls] cannot read CA bundle %s: %v", bundlePath, err)
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		Log.Warning("[tls] no valid certificates found in %s", bundlePath)
		return nil
	}
	Log.Info("[tls] loaded CA bundle from %s", bundlePath)
	return pool
}

// configureTrustStore sets the TLS client config on the global HTTP
// clients so outgoing connections trust the CA bundle and don't reject
// certificates that are missing SANs (common with internal PKI).
func configureTrustStore(caBundle *x509.CertPool) {
	tlsClientConfig := &tls.Config{
		RootCAs: caBundle, // nil means use system default
	}
	// don't reject certs without SANs: verify the chain ourselves
	// without checking ServerName, which avoids the Go stdlib error
	// "certificate relies on legacy Common Name field"
	if caBundle != nil {
		tlsClientConfig.InsecureSkipVerify = true
		tlsClientConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			opts := x509.VerifyOptions{
				Roots:         caBundle,
				Intermediates: x509.NewCertPool(),
			}
			for _, cert := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(cert)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		}
	}
	HTTPClient.Transport.(*TransformedTransport).Orig.(*http.Transport).TLSClientConfig = tlsClientConfig
	HTTP.Transport.(*TransformedTransport).Orig.(*http.Transport).TLSClientConfig = tlsClientConfig
}

func ensureAppHasBooted(address string, message string) {
	ensureAppHasBootedWith(http.DefaultClient, address, message)
}

func ensureAppHasBootedWith(client *http.Client, address string, message string) {
	for i := 0; i < 10; i++ {
		time.Sleep(250 * time.Millisecond)
		res, err := client.Get(address)
		if err != nil {
			continue
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNotFound {
			continue
		}
		Log.Info(message)
		return
	}
	Log.Warning("[http] didn't boot")
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
