//go:build integration

// Pins the linchpin of zero-downtime cert rotation: that the
// controller-runtime webhook server picks up a rewritten
// tls.crt/tls.key pair from CertDir without a process restart. The
// atomic-write contract on SelfSignedSource (write temp file +
// rename + fsync dir) is unit-tested in isolation under
// internal/certs/; this test pins the OTHER half - that the
// running webhook.Server actually reloads on the filesystem event
// the watcher fires.
//
// Without this spec, a regression in controller-runtime's
// certwatcher default behaviour (e.g. a future release dropping the
// implicit watcher when CertDir is set but no TLSOpts.GetCertificate
// is wired) would silently break rotation: unit tests stay green,
// production webhooks fail TLS handshake after every rotation, and
// failurePolicy=Ignore swallows the failure.
package manager_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/RemkoMolier/squirrel/internal/certs"
)

var _ = Describe("Webhook server reloads TLS on rotation", func() {
	It("serves a freshly-rotated leaf certificate without a process restart", func(ctx SpecContext) {
		// Bind to localhost on a kernel-picked port. We bind explicitly
		// and immediately close so that the webhook.Server can re-bind
		// to it; controller-runtime's server takes a port number, not
		// a listener.
		port, host := pickFreeTCPPort()

		certDir := GinkgoT().TempDir()

		// Initial CA + leaf. The webhook server reads tls.crt/tls.key
		// from certDir; we write a CA-signed leaf whose SAN matches
		// the host we dial so the TLS handshake completes.
		caA, leafA := mintCAAndLeaf(host)
		Expect(writeCertPair(certDir, leafA)).To(Succeed())

		// Stand up a Manager with a webhook.Server pointed at the
		// random port + the temp CertDir. Manager (rather than just
		// webhook.NewServer alone) buys us the manager-runnable
		// lifecycle so Start/Stop matches what production does.
		mgr, err := ctrl.NewManager(suiteCfg, ctrl.Options{
			Scheme:     suiteScheme,
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: ptrTrue()},
			WebhookServer: webhook.NewServer(webhook.Options{
				Host:    host,
				Port:    port,
				CertDir: certDir,
			}),
		})
		Expect(err).NotTo(HaveOccurred())

		// Register a no-op handler so the webhook server has at least
		// one path to expose. controller-runtime starts the TLS
		// listener once a handler is registered.
		mgr.GetWebhookServer().Register("/probe", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		mgrCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		started := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			close(started)
			if startErr := mgr.Start(mgrCtx); startErr != nil && ctx.Err() == nil {
				GinkgoLogr.Info("mgr.Start exited", "err", startErr)
			}
		}()
		<-started

		// Wait until the server is accepting TLS handshakes. The first
		// successful handshake also returns the leaf the server is
		// currently presenting; capture its serial so the post-rotation
		// handshake can be compared to it.
		var firstSerial string
		Eventually(func(g Gomega) {
			s, err := fetchServerLeafSerial(host, port, caA)
			g.Expect(err).NotTo(HaveOccurred())
			firstSerial = s
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		Expect(firstSerial).To(Equal(leafA.Cert().SerialNumber.String()))

		// Atomically rewrite tls.crt/tls.key with a fresh leaf signed
		// by a fresh CA. We swap the CA too because rotating just the
		// leaf would mask a regression where the watcher only fires
		// on tls.crt changes (the ca.crt write would silently land
		// without triggering reload).
		caB, leafB := mintCAAndLeaf(host)
		Expect(writeCertPair(certDir, leafB)).To(Succeed())

		// The watcher's debounce + filesystem event propagation means
		// we cannot synchronously observe the reload. Poll until the
		// post-rotation serial appears. The new client also has to
		// trust the new CA, so the handshake exercises BOTH the
		// server's reload AND that the new chain validates end-to-end.
		Eventually(func(g Gomega) {
			s, err := fetchServerLeafSerial(host, port, caB)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(s).To(Equal(leafB.Cert().SerialNumber.String()))
			g.Expect(s).NotTo(Equal(firstSerial))
		}, 15*time.Second, 100*time.Millisecond).Should(Succeed())
	})
})

// pickFreeTCPPort binds to ":0" on localhost, captures the kernel-
// assigned port number, closes the listener, and returns the port
// for the caller to re-bind. There is a race here in principle
// (another process could grab the port between our close and the
// webhook server's bind) but in practice the test runner is
// single-process and the window is microseconds.
func pickFreeTCPPort() (int, string) {
	GinkgoHelper()
	// Bind on 127.0.0.1 so the kernel picks an unused port, but
	// surface "localhost" to the caller so the cert SAN matches as
	// a DNS name (x509 IP SANs would need a separate IPAddresses
	// field; the certs package issues DNS-only SANs).
	const host = "localhost"
	l, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	port := l.Addr().(*net.TCPAddr).Port
	Expect(l.Close()).To(Succeed())
	return port, host
}

// mintCAAndLeaf returns a fresh ECDSA P-256 CA and a leaf cert
// signed by it whose SAN is "host". Used to drive the rotation
// scenario without standing up the full SelfSignedSource - the
// atomic-write half is exercised by SelfSignedSource tests in
// internal/certs.
func mintCAAndLeaf(host string) (*certs.CA, *certs.ServingCert) {
	GinkgoHelper()
	now := time.Now()
	ca, err := certs.NewSelfSignedCA("squirrel-test-ca", now, now.Add(24*time.Hour))
	Expect(err).NotTo(HaveOccurred())
	leaf, err := certs.IssueServingCert(ca, []string{host}, now, now.Add(24*time.Hour))
	Expect(err).NotTo(HaveOccurred())
	return ca, leaf
}

// writeCertPair atomically writes tls.crt + tls.key (and ca.crt for
// good measure) into dir. Uses the same temp+rename dance that
// SelfSignedSource uses in production; reproducing it here keeps
// the test independent of the source's internal atomicWriteFile
// helper.
func writeCertPair(dir string, leaf *certs.ServingCert) error {
	for _, f := range []struct {
		name string
		data []byte
	}{
		{certs.FileServingCert, leaf.CertPEM()},
		{certs.FileServingKey, leaf.KeyPEM()},
	} {
		path := filepath.Join(dir, f.name)
		tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
		if err != nil {
			return err
		}
		if _, err := tmp.Write(f.data); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	return nil
}

// fetchServerLeafSerial dials host:port over TLS using a root pool
// that trusts only ca, completes the handshake, and returns the
// leaf certificate's serial number as a decimal string. Returns
// an error if the dial, handshake, or any cert presence check
// fails.
func fetchServerLeafSerial(host string, port int, ca *certs.CA) (string, error) {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert())
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", fmt.Sprintf("%s:%d", host, port), &tls.Config{
		RootCAs:    pool,
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("no peer certs presented")
	}
	return state.PeerCertificates[0].SerialNumber.String(), nil
}

