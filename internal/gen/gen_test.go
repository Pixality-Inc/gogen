package gen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
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

const (
	testAssetMetadataName      = "AssetMetadata"
	testAssetMetadataInputName = "AssetMetadataInput"
	testPhotoMetadataName      = "PhotoMetadata"
	testVideoMetadataName      = "VideoMetadata"
)

func oneOfDiscriminatorTestModels(generator *Impl) (proto_parser.Model, proto_parser.Model, protoIndex) {
	photo := proto_parser.NewModel(0, "protocol", nil, testPhotoMetadataName,
		[]proto_parser.Field{proto_parser.NewField("url", "string")})
	video := proto_parser.NewModel(0, "protocol", nil, testVideoMetadataName,
		[]proto_parser.Field{proto_parser.NewField("url", "string")})

	oneOf := func() proto_parser.Field {
		return proto_parser.NewField("metadata", "oneOf",
			proto_parser.WithIsOneOf(),
			proto_parser.WithChildren([]proto_parser.Field{
				proto_parser.NewField("photo", testPhotoMetadataName),
				proto_parser.NewField("video", testVideoMetadataName),
			}),
		)
	}

	withType := proto_parser.NewModel(0, "protocol", nil, testAssetMetadataName,
		[]proto_parser.Field{proto_parser.NewField("type", "string"), oneOf()})
	withoutType := proto_parser.NewModel(0, "protocol", nil, testAssetMetadataInputName,
		[]proto_parser.Field{oneOf()})

	index := generator.buildProtoIndex(proto_parser.NewResult(
		map[string]proto_parser.Model{
			testAssetMetadataName:      withType,
			testAssetMetadataInputName: withoutType,
			testPhotoMetadataName:      photo,
			testVideoMetadataName:      video,
		},
		map[string]proto_parser.Enum{},
	))

	return withType, withoutType, index
}

func stubAddSchema(name string) (string, error) {
	return schemaName(name), nil
}

func TestModelSchemaOneOfDiscriminator(t *testing.T) {
	t.Parallel()

	generator, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	withType, _, index := oneOfDiscriminatorTestModels(generator)

	registered := map[string]*openapi3.SchemaRef{}
	registerSchema := func(name string, schemaRef *openapi3.SchemaRef) {
		registered[name] = schemaRef
	}

	ref, err := generator.modelSchema(withType, index, stubAddSchema, registerSchema)
	if err != nil {
		t.Fatalf("modelSchema: %v", err)
	}

	schema := ref.Value
	if schema.Discriminator == nil || schema.Discriminator.PropertyName != "type" {
		t.Fatalf("discriminator = %+v, want propertyName=type", schema.Discriminator)
	}

	if len(schema.OneOf) != 2 {
		t.Fatalf("oneOf members = %d, want 2", len(schema.OneOf))
	}

	if len(schema.Properties) != 0 {
		t.Fatalf("top-level properties = %v, want none (type is folded into variants)", schema.Properties)
	}

	const photoVariantSchema = "asset_metadata_oneof_photo"

	// children are sorted by name, so member 0 is "photo"; oneOf members must be plain refs so
	// the discriminator mapping resolves
	if got := schema.OneOf[0].Ref; got != "#/components/schemas/"+photoVariantSchema {
		t.Fatalf("photo oneOf member ref = %q, want %s", got, photoVariantSchema)
	}

	if got, ok := schema.Discriminator.Mapping["photo"]; !ok || got.Ref != "#/components/schemas/"+photoVariantSchema {
		t.Fatalf("photo mapping = %+v, want ref %s", got, photoVariantSchema)
	}

	wrapper := registered[photoVariantSchema]
	if wrapper == nil {
		t.Fatalf("variant schema %s was not registered", photoVariantSchema)
	}

	if got := wrapper.Value.Required; len(got) != 2 || got[0] != "type" || got[1] != "photo" {
		t.Fatalf("photo variant required = %v, want [type photo]", got)
	}

	typeProp := wrapper.Value.Properties["type"]
	if typeProp == nil || typeProp.Value.Type == nil || !typeProp.Value.Type.Is(openapi3.TypeString) {
		t.Fatalf("photo variant type prop is not a string: %+v", typeProp)
	}

	if len(typeProp.Value.Enum) != 1 || typeProp.Value.Enum[0] != "photo" {
		t.Fatalf("photo variant type enum = %v, want [photo]", typeProp.Value.Enum)
	}

	contentRef := wrapper.Value.Properties["photo"]
	if contentRef == nil || contentRef.Ref != "#/components/schemas/photo_metadata" {
		t.Fatalf("photo variant content ref = %+v, want $ref photo_metadata", contentRef)
	}
}

func TestModelSchemaOneOfWithoutTypeReturnsError(t *testing.T) {
	t.Parallel()

	generator, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	_, withoutType, index := oneOfDiscriminatorTestModels(generator)

	registerSchema := func(string, *openapi3.SchemaRef) {}

	// a oneof message without a string discriminator field is a hard generation error,
	// never a silently-untagged union
	_, err = generator.modelSchema(withoutType, index, stubAddSchema, registerSchema)
	if !errors.Is(err, errOneOfWithoutDiscriminator) {
		t.Fatalf("modelSchema error = %v, want errOneOfWithoutDiscriminator", err)
	}
}

const testAssetMetadataTypeEnum = "AssetMetadataType"

func oneOfEnumDiscriminatorTestModel(generator *Impl) (proto_parser.Model, protoIndex) {
	photo := proto_parser.NewModel(0, "protocol", nil, testPhotoMetadataName,
		[]proto_parser.Field{proto_parser.NewField("url", "string")})
	video := proto_parser.NewModel(0, "protocol", nil, testVideoMetadataName,
		[]proto_parser.Field{proto_parser.NewField("url", "string")})

	oneOf := proto_parser.NewField("metadata", "oneOf",
		proto_parser.WithIsOneOf(),
		proto_parser.WithChildren([]proto_parser.Field{
			proto_parser.NewField("photo", testPhotoMetadataName),
			proto_parser.NewField("video", testVideoMetadataName),
		}),
	)

	withEnumType := proto_parser.NewModel(0, "protocol", nil, testAssetMetadataName,
		[]proto_parser.Field{proto_parser.NewField("type", testAssetMetadataTypeEnum), oneOf})

	enum := proto_parser.NewEnum(0, "protocol", nil, testAssetMetadataTypeEnum, []proto_parser.EnumEntry{
		proto_parser.NewEnumEntry("ASSET_METADATA_TYPE_UNKNOWN", 0, ""),
		proto_parser.NewEnumEntry("ASSET_METADATA_TYPE_PHOTO", 1, ""),
		proto_parser.NewEnumEntry("ASSET_METADATA_TYPE_VIDEO", 2, ""),
	})

	index := generator.buildProtoIndex(proto_parser.NewResult(
		map[string]proto_parser.Model{
			testAssetMetadataName: withEnumType,
			testPhotoMetadataName: photo,
			testVideoMetadataName: video,
		},
		map[string]proto_parser.Enum{
			testAssetMetadataTypeEnum: enum,
		},
	))

	return withEnumType, index
}

func TestModelSchemaOneOfEnumDiscriminator(t *testing.T) {
	t.Parallel()

	generator, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	withEnumType, index := oneOfEnumDiscriminatorTestModel(generator)

	registered := map[string]*openapi3.SchemaRef{}
	registerSchema := func(name string, schemaRef *openapi3.SchemaRef) {
		registered[name] = schemaRef
	}

	ref, err := generator.modelSchema(withEnumType, index, stubAddSchema, registerSchema)
	if err != nil {
		t.Fatalf("modelSchema: %v", err)
	}

	schema := ref.Value
	if schema.Discriminator == nil || schema.Discriminator.PropertyName != "type" {
		t.Fatalf("discriminator = %+v, want propertyName=type", schema.Discriminator)
	}

	const (
		photoVariantSchema = "asset_metadata_oneof_photo"
		photoEnumValue     = "ASSET_METADATA_TYPE_PHOTO"
	)

	// the discriminator value tags the variant with its enum value name, not the variant name
	got, ok := schema.Discriminator.Mapping[photoEnumValue]
	if !ok || got.Ref != "#/components/schemas/"+photoVariantSchema {
		t.Fatalf("photo mapping[%s] = %+v, want ref %s", photoEnumValue, got, photoVariantSchema)
	}

	wrapper := registered[photoVariantSchema]
	if wrapper == nil {
		t.Fatalf("variant schema %s was not registered", photoVariantSchema)
	}

	typeProp := wrapper.Value.Properties["type"]
	if typeProp == nil || typeProp.Value.Type == nil || !typeProp.Value.Type.Is(openapi3.TypeString) {
		t.Fatalf("photo variant type prop is not a string: %+v", typeProp)
	}

	if len(typeProp.Value.Enum) != 1 || typeProp.Value.Enum[0] != photoEnumValue {
		t.Fatalf("photo variant type enum = %v, want [%s]", typeProp.Value.Enum, photoEnumValue)
	}
}

func TestModelSchemaBaseDiscriminatedOneOf(t *testing.T) {
	t.Parallel()

	generator, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}

	photo := proto_parser.NewModel(0, "protocol", nil, testPhotoMetadataName,
		[]proto_parser.Field{proto_parser.NewField("url", "string")})
	video := proto_parser.NewModel(0, "protocol", nil, testVideoMetadataName,
		[]proto_parser.Field{proto_parser.NewField("url", "string")})

	oneOf := proto_parser.NewField("payload", "oneOf",
		proto_parser.WithIsOneOf(),
		proto_parser.WithChildren([]proto_parser.Field{
			proto_parser.NewField("photo", testPhotoMetadataName),
			proto_parser.NewField("video", testVideoMetadataName),
		}),
	)

	// a message with common fields (id) + an enum discriminator (type) + a oneof renders as a shared
	// base plus one allOf variant per enum value
	asset := proto_parser.NewModel(0, "protocol", nil, "Asset", []proto_parser.Field{
		proto_parser.NewField("id", "string"),
		proto_parser.NewField("type", "AssetType"),
		oneOf,
	})

	enum := proto_parser.NewEnum(0, "protocol", nil, "AssetType", []proto_parser.EnumEntry{
		proto_parser.NewEnumEntry("ASSET_TYPE_UNKNOWN", 0, ""),
		proto_parser.NewEnumEntry("ASSET_TYPE_PHOTO", 1, ""),
		proto_parser.NewEnumEntry("ASSET_TYPE_VIDEO", 2, ""),
		proto_parser.NewEnumEntry("ASSET_TYPE_COMMENT", 3, ""), // thin: no matching oneof arm
	})

	index := generator.buildProtoIndex(proto_parser.NewResult(
		map[string]proto_parser.Model{
			"Asset":               asset,
			testPhotoMetadataName: photo,
			testVideoMetadataName: video,
		},
		map[string]proto_parser.Enum{
			"AssetType": enum,
		},
	))

	registered := map[string]*openapi3.SchemaRef{}
	registerSchema := func(name string, schemaRef *openapi3.SchemaRef) {
		registered[name] = schemaRef
	}

	ref, err := generator.modelSchema(asset, index, stubAddSchema, registerSchema)
	if err != nil {
		t.Fatalf("modelSchema: %v", err)
	}

	if ref.Value.Discriminator == nil || ref.Value.Discriminator.PropertyName != "type" {
		t.Fatalf("discriminator = %+v, want propertyName=type", ref.Value.Discriminator)
	}

	// the base carries the common fields but neither the discriminator nor the oneof arms
	base := registered["asset_base"]
	if base == nil {
		t.Fatalf("asset_base was not registered")
	}

	if base.Value.Properties["id"] == nil {
		t.Fatalf("asset_base missing common field id")
	}

	if base.Value.Properties["type"] != nil {
		t.Fatalf("asset_base must not carry the discriminator field")
	}

	// fat variant: allOf[base, {type const, arm}]
	photoVariant := registered["photo_asset"]
	if photoVariant == nil {
		t.Fatalf("photo_asset variant was not registered")
	}

	if len(photoVariant.Value.AllOf) != 2 {
		t.Fatalf("photo_asset allOf len = %d, want 2", len(photoVariant.Value.AllOf))
	}

	if photoVariant.Value.AllOf[0].Ref != "#/components/schemas/asset_base" {
		t.Fatalf("photo_asset first allOf member = %q, want asset_base ref", photoVariant.Value.AllOf[0].Ref)
	}

	fatProps := photoVariant.Value.AllOf[1].Value.Properties
	if fatProps["photo"] == nil {
		t.Fatalf("photo_asset variant missing photo arm")
	}

	if len(fatProps["type"].Value.Enum) != 1 || fatProps["type"].Value.Enum[0] != "ASSET_TYPE_PHOTO" {
		t.Fatalf("photo_asset type const = %v, want [ASSET_TYPE_PHOTO]", fatProps["type"].Value.Enum)
	}

	// thin variant (enum value with no matching arm): allOf[base, {type const}] only
	commentVariant := registered["comment_asset"]
	if commentVariant == nil {
		t.Fatalf("comment_asset thin variant was not registered")
	}

	thinProps := commentVariant.Value.AllOf[1].Value.Properties
	if len(thinProps) != 1 || thinProps["type"] == nil {
		t.Fatalf("comment_asset must carry only the type const, got %v", thinProps)
	}

	// the zero (unknown) enum value is excluded from the union
	if _, ok := ref.Value.Discriminator.Mapping["ASSET_TYPE_UNKNOWN"]; ok {
		t.Fatalf("unknown enum value must not be mapped")
	}

	if got := ref.Value.Discriminator.Mapping["ASSET_TYPE_PHOTO"]; got.Ref != "#/components/schemas/photo_asset" {
		t.Fatalf("photo mapping = %+v, want photo_asset ref", got)
	}
}

func TestEnumPropertyRendersStringNames(t *testing.T) {
	t.Parallel()

	enum := proto_parser.NewEnum(0, "protocol", nil, "AssetType", []proto_parser.EnumEntry{
		proto_parser.NewEnumEntry("ASSET_TYPE_UNKNOWN", 0, ""),
		proto_parser.NewEnumEntry("ASSET_TYPE_PHOTO", 2, ""),
		proto_parser.NewEnumEntry("ASSET_TYPE_VIDEO", 1, ""),
	})

	schema := enumProperty(enum, propertyExtras{}).Value
	if schema.Type == nil || !schema.Type.Is(openapi3.TypeString) {
		t.Fatalf("enum schema type = %+v, want string", schema.Type)
	}

	if schema.Format != "" {
		t.Fatalf("enum schema format = %q, want empty", schema.Format)
	}

	// values are sorted by numeric value: UNKNOWN(0), VIDEO(1), PHOTO(2)
	want := []any{"ASSET_TYPE_UNKNOWN", "ASSET_TYPE_VIDEO", "ASSET_TYPE_PHOTO"}
	if len(schema.Enum) != len(want) {
		t.Fatalf("enum values = %v, want %v", schema.Enum, want)
	}

	for i := range want {
		if schema.Enum[i] != want[i] {
			t.Fatalf("enum[%d] = %v, want %v", i, schema.Enum[i], want[i])
		}
	}

	if _, ok := schema.Extensions["x-enum-varnames"]; ok {
		t.Fatal("x-enum-varnames should be removed")
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
