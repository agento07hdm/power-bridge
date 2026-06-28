package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"time"
)

// ListenTLSCaptivePortal opens an HTTPS listener on addr with a self-signed
// in-memory certificate for 192.168.4.1.  In AP mode every request is
// redirected 302 to http://192.168.4.1/wifi so that Android 10+ and iOS 14+
// captive-portal checkers (which probe HTTPS first) are caught.  Outside AP
// mode the server replies 204 so devices do not show a "no internet" warning.
func (s *Server) ListenTLSCaptivePortal(addr string) error {
	cert, err := generateSelfSignedCert()
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isAPMode() {
			http.Redirect(w, r, "http://"+apModeIP+"/wifi", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	tlsLn := tls.NewListener(ln, srv.TLSConfig)
	return srv.Serve(tlsLn)
}

func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: apModeIP},
		IPAddresses:  []net.IP{net.ParseIP(apModeIP)},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
