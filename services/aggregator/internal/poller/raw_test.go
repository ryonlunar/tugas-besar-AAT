package poller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRawIntakePreservesUndecodableResponse(t *testing.T) {
	for _, body := range [][]byte{[]byte(`[{"report_id":`), {'[', '{', '"', 'x', '"', ':', '"', 0xff, '"', '}', ']'}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
		client := NewPVMBGClient(server.URL, "token", server.Client(), 1<<20)
		records, err := client.ReadRecords(context.Background())
		server.Close()
		if err != nil || len(records) != 1 || !bytes.Equal(records[0].Payload, body) && !bytes.Equal(records[0].Payload, body[1:len(body)-1]) {
			t.Fatalf("raw bytes changed before persistence: %+v %v", records, err)
		}
	}
}
