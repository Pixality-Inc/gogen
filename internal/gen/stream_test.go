package gen

import (
	"context"
	"errors"
	"testing"

	"github.com/pixality-inc/gogen/internal/config"
)

const streamTestProtoFile = "protocol.proto"

const streamTestProto = `syntax = "proto3";

package protocol;

option go_package = "./internal/protocol";

message ErrorResponse {
  string message = 1;
}

message OkResponse {
}
`

const streamTestRoutes = `---
api:
  info:
    title: Test API
    version: "1.0"
  security:
    bearer:
      type: bearer
      model: '*auth.Metadata'
      modelGetter: 'middlewares.GetClientAuthMetadata'
  routerImports:
    - ["", "github.com/pixality-inc/golang-core/http"]
    - ["", "example/internal/api/controllers"]
  controllerImports:
    - ["", "example/internal/protocol"]
  routes:
    /proxy/{path:*}:
      post:
        id: ProxyForward
        tags: [proxy]
        title: Forward to the upstream
        security: [bearer]
        authRequired: true
        stream: true
        rawBody: true
        parameters:
          path:
            in: path
            type: string
            required: true
          X-Proxy-Key:
            in: header
            type: string
        responseModels: ["400", "401"]
  errors:
    400: "protocol.ErrorResponse"
    401: "protocol.ErrorResponse"
`

func generateStreamTestApi(t *testing.T, routes string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	writeTestFile(t, dir, streamTestProtoFile, streamTestProto)
	writeTestFile(t, dir, "gen/api.yaml", routes)

	generator, err := New(&config.Config{
		Gen: config.Gen{
			Settings: config.Settings{WorkDir: dir},
			Api: config.Api{
				ProtoFiles:   []string{streamTestProtoFile},
				ModelsPrefix: "protocol.",
			},
		},
	})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	return dir, generator.Generate(context.Background(), SwaggerGenerator, ApiGenerator)
}

func TestGenerateStreamOperation(t *testing.T) {
	t.Parallel()

	dir, err := generateStreamTestApi(t, streamTestRoutes)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	controller := "internal/api/controllers/controller_gen.go"
	assertFileContains(t, dir, controller, "ProxyForward(ctx context.Context, params ProxyForwardRequest) (http.StreamResponse, error)")
	assertFileContains(t, dir, controller, `"github.com/pixality-inc/golang-core/http"`)
	assertFileContains(t, dir, controller, "RawBody   []byte")
	assertFileContains(t, dir, controller, "Path      string")

	handler := "internal/api/request_handler_gen.go"
	assertFileContains(t, dir, handler, `router.POST("/proxy/{path:*}", handleWithController(controller, handleProxyForward))`)
	assertFileContains(t, dir, handler, `param, ok := ctx.UserValue("path").(string)`)
	assertFileContains(t, dir, handler, "http.HandleStream(ctx, response)")
	assertFileNotContains(t, dir, handler, "http.Ok(ctx, response)")

	swagger := "docs/swagger.yaml"
	assertFileContains(t, dir, swagger, "/proxy/{path}:")
	assertFileNotContains(t, dir, swagger, "{path:*}")
	assertFileContains(t, dir, swagger, "text/event-stream:")
	assertFileContains(t, dir, swagger, "description: Streamed response")
	assertFileContains(t, dir, swagger, "'*/*':")
	assertFileContains(t, dir, swagger, "format: binary")
}

func TestGenerateStreamOperationRejectsHttpAndSuccessModel(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		routes   string
		expected error
	}{
		{
			name: "http and stream",
			routes: `---
api:
  routes:
    /events:
      get:
        id: EventsStream
        stream: true
        http: true
        responseModels: ["400"]
  errors:
    400: "protocol.ErrorResponse"
`,
			expected: errStreamWithHTTP,
		},
		{
			name: "success model",
			routes: `---
api:
  routes:
    /events:
      get:
        id: EventsStream
        stream: true
        responseModels: ["400", "protocol.OkResponse"]
  errors:
    400: "protocol.ErrorResponse"
`,
			expected: errStreamResponseModel,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := generateStreamTestApi(t, testCase.routes)
			if !errors.Is(err, testCase.expected) {
				t.Fatalf("expected %v, got %v", testCase.expected, err)
			}
		})
	}
}

func TestSwaggerRoutePath(t *testing.T) {
	t.Parallel()

	testCases := map[string]string{
		"/books":                      "/books",
		"/books/{id}":                 "/books/{id}",
		"/proxy/{path:*}":             "/proxy/{path}",
		"/orgs/{orgId}/files/{key:*}": "/orgs/{orgId}/files/{key}",
		"/files/{name:[a-z]+}.json":   "/files/{name}.json",
		"/broken/{open":               "/broken/{open",
	}

	for url, expected := range testCases {
		if actual := swaggerRoutePath(url); actual != expected {
			t.Errorf("swaggerRoutePath(%q) = %q, expected %q", url, actual, expected)
		}
	}
}
