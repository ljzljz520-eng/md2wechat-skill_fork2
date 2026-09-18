package wechat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/geekjourneyx/md2wechat-skill/internal/config"
	"go.uber.org/zap"
)

func newProbeService(t *testing.T, probeResp *http.Response, probeErr error) (*Service, *int) {
	t.Helper()
	calls := 0
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "/cgi-bin/token"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"token-123","expires_in":7200}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			case strings.Contains(req.URL.String(), "/cgi-bin/material/get_material"):
				calls++
				if !strings.Contains(req.URL.String(), "access_token=token-123") {
					t.Fatalf("probe url missing access token: %s", req.URL.String())
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read probe request body: %v", err)
				}
				if !strings.Contains(string(body), `"media_id":"media-to-probe"`) {
					t.Fatalf("probe request body = %s", string(body))
				}
				if probeErr != nil {
					return nil, probeErr
				}
				resp := &http.Response{}
				*resp = *probeResp
				resp.Request = req
				if resp.Header == nil {
					resp.Header = make(http.Header)
				}
				return resp, nil
			default:
				t.Fatalf("unexpected request url: %s", req.URL.String())
				return nil, nil
			}
		}),
	}

	svc := &Service{
		cfg: &config.Config{
			WechatAppID:  "appid",
			WechatSecret: "secret",
		},
		log:        zap.NewNop(),
		httpClient: client,
	}
	return svc, &calls
}

func TestMaterialExistsClassifiesResponses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name     string
		response *http.Response
		err      error
		want     bool
		wantErr  bool
	}{
		{
			name: "image binary exists",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("\xff\xd8\xff\xe0\x00\x10JFIFbinary")),
			},
			want: true,
		},
		{
			name: "errcode zero json",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"errcode":0,"errmsg":"ok"}`)),
			},
			want: true,
		},
		{
			name: "errcode 40007 invalid media id",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"errcode":40007,"errmsg":"invalid media_id"}`)),
			},
			want: false,
		},
		{
			name: "errcode 40009 invalid image",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"errcode":40009,"errmsg":"invalid image"}`)),
			},
			want: false,
		},
		{
			name:    "probe network failure",
			err:     fmt.Errorf("dial tcp: i/o timeout"),
			want:    false,
			wantErr: true,
		},
		{
			name: "unknown errcode returns error",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"errcode":45009,"errmsg":"rate limited"}`)),
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "non-200 status returns error",
			response: &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader(`server error`)),
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "empty body returns error",
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
			},
			want:    false,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, calls := newProbeService(t, tc.response, tc.err)
			got, err := svc.MaterialExists(ctx, "media-to-probe")
			if got != tc.want {
				t.Fatalf("exists = %v, want %v", got, tc.want)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if *calls != 1 {
				t.Fatalf("probe calls = %d, want 1", *calls)
			}
		})
	}
}

func TestMaterialExistsRejectsEmptyMediaIDBeforeNetwork(t *testing.T) {
	svc, calls := newProbeService(t, &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("x")),
	}, nil)

	_, err := svc.MaterialExists(context.Background(), "  ")
	if err == nil {
		t.Fatal("expected error for empty media id")
	}
	if *calls != 0 {
		t.Fatalf("probe calls = %d, want 0", *calls)
	}
}

func TestMaterialExistsUsesInjectedSeam(t *testing.T) {
	svc := &Service{
		log: zap.NewNop(),
		probeMaterialFunc: func(ctx context.Context, mediaID string) (bool, error) {
			if mediaID != "seam-id" {
				t.Fatalf("media id = %q", mediaID)
			}
			return false, nil
		},
	}

	got, err := svc.MaterialExists(context.Background(), "seam-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("exists = true, want false")
	}
}

func TestIsInvalidMediaError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"40007", fmt.Errorf("wechat api error: 40007 - invalid media id"), true},
		{"40009", fmt.Errorf("wechat api error: 40009 - invalid image size"), true},
		{"45002 draft limit", fmt.Errorf("wechat api error: 45002 - content out of limit"), false},
		{"plain network error", errors.New("dial tcp: connection refused"), false},
		{"bare code token", errors.New("40007"), true},
		{"unrelated message", errors.New("upload material: failed"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsInvalidMediaError(tc.err); got != tc.want {
				t.Fatalf("IsInvalidMediaError() = %v, want %v", got, tc.want)
			}
		})
	}
}
