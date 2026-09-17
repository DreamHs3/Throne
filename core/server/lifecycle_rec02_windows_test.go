//go:build windows

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"ThroneCore/gen"
	"golang.org/x/sys/windows/svc"
	"google.golang.org/protobuf/proto"
)

// REC-02 R3 regression: the SECOND Start must return an error while the
// running runtime, its cancel and the redirect mark stay untouched, and a
// subsequent Stop must close that runtime. The pre-fix deferred cleanup ran
// for the rejected duplicate Start too and orphaned the live box.
func TestDuplicateStartPreservesRunningRuntime(t *testing.T) {
	t.Cleanup(func() {
		_, _ = globalServer.Stop(context.Background(), &gen.EmptyReq{})
		autoRedirectMark.Store(0)
	})

	req := &gen.LoadConfigReq{CoreConfig: proto.String(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`)}
	// The legacy Start dereferences proto2 optional fields directly; the
	// service path materializes the defaults through normalizeLoadConfigReq,
	// and this test drives the legacy path directly, so it does the same.
	normalizeLoadConfigReq(req)
	if _, err := globalServer.Start(context.Background(), req); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if currentBox() == nil {
		t.Fatal("the first start did not publish the runtime")
	}
	autoRedirectMark.Store(7777)

	resp, err := globalServer.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("duplicate start returned a transport error: %v", err)
	}
	if resp.GetError() != "instance already started" {
		t.Fatalf("duplicate start error = %q, want %q", resp.GetError(), "instance already started")
	}
	if currentBox() == nil {
		t.Fatal("the duplicate Start erased the running runtime reference (REC-02 R3)")
	}
	if got := autoRedirectMark.Load(); got != 7777 {
		t.Fatalf("the duplicate Start reset the redirect mark: got %d, want 7777 (REC-02 R3)", got)
	}

	if _, err := globalServer.Stop(context.Background(), &gen.EmptyReq{}); err != nil {
		t.Fatalf("stop after duplicate start: %v", err)
	}
	if currentBox() != nil {
		t.Fatal("the subsequent stop did not close the runtime")
	}
}
