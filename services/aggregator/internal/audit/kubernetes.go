package audit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
	"io"
	"net/http"
	"os"
	"time"
)

type Pod struct {
	Name, Unit, Node, IP, Role string
	Ready                      bool
}
type Topology func(context.Context) ([]Pod, error)

// KubernetesTopology needs only namespace-scoped pod-list permission. The
// projected service-account token is reread so token rotation keeps working.
func KubernetesTopology() Topology {
	const directory = "/var/run/secrets/kubernetes.io/serviceaccount/"
	ca, caErr := os.ReadFile(directory + "ca.crt")
	roots := x509.NewCertPool()
	validCA := roots.AppendCertsFromPEM(ca)
	client := &http.Client{Timeout: time.Second, Transport: telemetry.HTTPTransport(&http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context) ([]Pod, error) {
		if caErr != nil || !validCA {
			return nil, errors.New("Kubernetes CA unavailable")
		}
		token, err := os.ReadFile(directory + "token")
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://kubernetes.default.svc/api/v1/namespaces/sistem-informasi-kebencanaan-bnpb/pods?labelSelector=role%3Dstore", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+string(token))
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, errors.New("Kubernetes pod discovery unavailable")
		}
		var body struct {
			Items []struct {
				Metadata struct {
					Name   string
					Labels map[string]string
				}
				Spec   struct{ NodeName string }
				Status struct {
					PodIP      string
					Conditions []struct{ Type, Status string }
				}
			}
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil {
			return nil, err
		}
		pods := []Pod{}
		for _, p := range body.Items {
			role := "replica"
			if p.Metadata.Labels["postgres-role"] == "master" {
				role = "primary"
			}
			ready := false
			for _, c := range p.Status.Conditions {
				if c.Type == "Ready" && c.Status == "True" {
					ready = true
				}
			}
			pods = append(pods, Pod{Name: p.Metadata.Name, Unit: p.Metadata.Labels["cluster-name"], Node: p.Spec.NodeName, IP: p.Status.PodIP, Role: role, Ready: ready})
		}
		return pods, nil
	}
}
