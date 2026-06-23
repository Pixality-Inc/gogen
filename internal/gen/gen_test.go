package gen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pixality-inc/gogen/internal/config"
	"github.com/pixality-inc/golang-core/proto_parser"
)

const testOrgInvitationName = "OrgInvitation"

func TestGenerateAllFromYamlAndProto(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTestFile(t, dir, "protocol.proto", `syntax = "proto3";

package protocol;

option go_package = "./internal/protocol";

message ErrorResponse {
  string message = 1;
}

message Book {
  option deprecated = true;

  string id = 1;
  string title = 2;
  BookVisibility visibility = 3;
}

message BooksResponse {
  int32 total = 1;
  repeated Book books = 2;
}

enum BookVisibility {
  option deprecated = true;
  reserved 2;

  BOOK_VISIBILITY_UNKNOWN = 0;
  BOOK_VISIBILITY_PUBLIC = 1;
  BOOK_VISIBILITY_PRIVATE = 3;
}
`)
	writeTestFile(t, dir, "gen/api.yaml", `---
api:
  info:
    title: Test API
    version: "1.0"
  servers:
    - url: ""
      description: Current Host
  imports: []
  routerImports:
    - ["", "github.com/pixality-inc/golang-core/http"]
    - ["", "example/internal/api/controllers"]
  controllerImports:
    - ["", "example/internal/protocol"]
  routes:
    /books:
      get:
        id: BooksGet
        tags: [books]
        title: Get books
        responseModels: ["400", "protocol.BooksResponse"]
    /books/{id}:
      get:
        id: BookGet
        tags: [books]
        title: Get book
        parameters:
          id:
            in: path
            type: string
            format: uuid
            required: true
        responseModels: ["400", "protocol.Book"]
  errors:
    400: "protocol.ErrorResponse"
`)
	writeTestFile(t, dir, "gen/enums.yaml", `---
enums:
  book_status:
    - draft
    - published
`)
	writeTestFile(t, dir, "gen/dao/books.yaml", `---
name: books
model_name: book
dao_imports:
  - ["", "example/internal/types"]
fields:
  - name: id
    type: uuid
    primary: true
    data_type: types.BookId
    default: gen_random_uuid()
  - name: title
    type: varchar(255)
  - name: created_at
    type: timestamp
    default: NOW()
`)
	writeTestFile(t, dir, "gen/ids.yaml", `---
ids:
  - name: book
`)

	generator, err := New(&config.Config{
		Gen: config.Gen{
			Settings: config.Settings{WorkDir: dir},
			Api: config.Api{
				ProtoFiles:   []string{"protocol.proto"},
				ModelsPrefix: "protocol.",
			},
		},
	})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	if err = generator.Generate(context.Background()); err != nil {
		t.Fatalf("generate: %v", err)
	}

	assertFileContains(t, dir, "docs/swagger.yaml", "openapi: 3.0.0")
	assertFileNotExists(t, dir, "internal/api/gen.go")
	assertFileContains(t, dir, "internal/api/controllers/controller_gen.go", "BooksGet(ctx context.Context) (*protocol.BooksResponse, error)")
	assertFileContains(t, dir, "internal/api/controllers/controller_gen.go", "Id uuid.UUID")
	assertFileContains(t, dir, "internal/api/request_handler_gen.go", "paramValue, err := http.ParseUUID(param)")
	assertFileContains(t, dir, "internal/dao/books.go", "GetById(ctx context.Context, queryRunner postgres.QueryRunner, id types.BookId)")
	assertFileContains(t, dir, "internal/types/book_gen.go", "type BookId uuid.UUID")
	assertFileContains(t, dir, "migrations/models/books_gen.sql", `CREATE TABLE IF NOT EXISTS "books"`)
}

func TestResolveModelPrefersCurrentMessageScope(t *testing.T) {
	t.Parallel()

	generator, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	orgInvitation := proto_parser.NewModel(0, "protocol", nil, testOrgInvitationName, nil)
	orgInvitationDates := proto_parser.NewModel(0, "protocol", []string{testOrgInvitationName}, "Dates", nil)
	assetMetadataDates := proto_parser.NewModel(
		0,
		"protocol",
		[]string{"AssetMetadata", "SystemMessage"},
		"Dates",
		nil,
	)

	index := protoIndex{
		modelsByKey: map[string]proto_parser.Model{
			"Dates":                               assetMetadataDates,
			"OrgInvitation__Dates":                orgInvitationDates,
			"AssetMetadata__SystemMessage__Dates": assetMetadataDates,
		},
	}

	model, ok := generator.resolveModel(orgInvitation, "Dates", index)
	if !ok {
		t.Fatal("resolve Dates")
	}

	if model != orgInvitationDates {
		t.Fatalf("Dates resolved to %s, want OrgInvitation Dates", generator.apiModelName(model))
	}

	model, ok = generator.resolveModel(orgInvitation, ".protocol.OrgInvitation.Dates", index)
	if !ok {
		t.Fatal("resolve fully qualified Dates")
	}

	if model != orgInvitationDates {
		t.Fatalf("fully qualified Dates resolved to %s, want OrgInvitation Dates", generator.apiModelName(model))
	}
}

func TestResolveModelPrefersNestedCurrentMessageScope(t *testing.T) {
	t.Parallel()

	generator, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	settings := proto_parser.NewModel(0, "protocol", []string{testOrgInvitationName}, "Settings", nil)
	settingsFlags := proto_parser.NewModel(0, "protocol", []string{testOrgInvitationName, "Settings"}, "Flags", nil)
	assetMetadataFlags := proto_parser.NewModel(
		0,
		"protocol",
		[]string{"AssetMetadata", "InputMeasurement"},
		"Flags",
		nil,
	)

	index := protoIndex{
		modelsByKey: map[string]proto_parser.Model{
			"Flags":                                  assetMetadataFlags,
			"OrgInvitation__Settings__Flags":         settingsFlags,
			"AssetMetadata__InputMeasurement__Flags": assetMetadataFlags,
		},
	}

	model, ok := generator.resolveModel(settings, "Flags", index)
	if !ok {
		t.Fatal("resolve Flags")
	}

	if model != settingsFlags {
		t.Fatalf("Flags resolved to %s, want OrgInvitation Settings Flags", generator.apiModelName(model))
	}
}

func TestBuildProtoIndexDoesNotRegisterNestedTypesByBareName(t *testing.T) {
	t.Parallel()

	generator, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	book := proto_parser.NewModel(0, "protocol", nil, "Book", nil)
	dates := proto_parser.NewModel(0, "protocol", []string{testOrgInvitationName}, "Dates", nil)
	status := proto_parser.NewEnum(0, "protocol", nil, "Status", nil)
	flags := proto_parser.NewEnum(0, "protocol", []string{testOrgInvitationName}, "Flags", nil)

	index := generator.buildProtoIndex(proto_parser.NewResult(
		map[string]proto_parser.Model{
			"Book":                 book,
			"OrgInvitation__Dates": dates,
		},
		map[string]proto_parser.Enum{
			"Status":               status,
			"OrgInvitation__Flags": flags,
		},
	))

	if model, ok := index.modelsByKey["Book"]; !ok || model != book {
		t.Fatal("top-level model Book is not indexed by bare name")
	}

	if _, ok := index.modelsByKey["Dates"]; ok {
		t.Fatal("nested model Dates is indexed by bare name")
	}

	if enum, ok := index.enumsByName["Status"]; !ok || enum != status {
		t.Fatal("top-level enum Status is not indexed by bare name")
	}

	if _, ok := index.enumsByName["Flags"]; ok {
		t.Fatal("nested enum Flags is indexed by bare name")
	}
}

func writeTestFile(t *testing.T, root string, name string, content string) {
	t.Helper()

	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filename, err)
	}

	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

func assertFileContains(t *testing.T, root string, name string, needle string) {
	t.Helper()

	content := readTestFile(t, root, name)
	if !strings.Contains(content, needle) {
		t.Fatalf("%s does not contain %q\n%s", name, needle, content)
	}
}

func assertFileNotExists(t *testing.T, root string, name string) {
	t.Helper()

	filename := filepath.Join(root, filepath.FromSlash(name))
	if _, err := os.Stat(filename); err == nil {
		t.Fatalf("%s exists", name)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", name, err)
	}
}

func readTestFile(t *testing.T, root string, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}

	return string(content)
}
