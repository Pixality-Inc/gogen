package gen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gobeam/stringy"
	"github.com/oasdiff/yaml"
	"github.com/pixality-inc/gogen/internal/config"
	gentemplate "github.com/pixality-inc/gogen/internal/template"
	"github.com/pixality-inc/golang-core/logger"
	"github.com/pixality-inc/golang-core/proto_parser"
	"github.com/pixality-inc/golang-core/storage"
	"github.com/pixality-inc/golang-core/storage/providers"
	"github.com/pixality-inc/golang-core/timetrack"
	yamlv3 "gopkg.in/yaml.v3"
)

var (
	ErrProtoParse        = errors.New("proto parse error")
	ErrGeneratorNotFount = errors.New("generator not found")

	errUnknownSecurityType            = errors.New("unknown security type")
	errOnlyOneSecurityAllowed         = errors.New("only 1 security is allowed for operation")
	errUnknownRouteOperation          = errors.New("unknown route operation")
	errUnknownParameterType           = errors.New("unknown parameter type")
	errUnknownParameterFormat         = errors.New("unknown parameter format")
	errUnknownParameterIn             = errors.New("unknown parameter 'in'")
	errNoModelGetterSet               = errors.New("no modelGetter set for parameter")
	errParameterArrayNotSupported     = errors.New("parameter array is not supported for this type")
	errSecurityNotFound               = errors.New("can't find security")
	errNoSchemaFound                  = errors.New("no schema found for model")
	errUnknownFieldType               = errors.New("unknown field type")
	errInvalidJSONField               = errors.New("invalid json field")
	errInvalidEnumField               = errors.New("invalid enum field")
	errEnumNotFound                   = errors.New("enum not found")
	errInvalidFieldType               = errors.New("invalid field type")
	errUnsupportedFieldType           = errors.New("unsupported field type")
	errReferencesMustContainTwoValues = errors.New("references property must contain 2 values only")
	errInvalidImport                  = errors.New("invalid import")
	errUnsupportedIDType              = errors.New("unsupported id type")
	errAPISchemaFileNotFound          = errors.New("api schema file does not exist")
	errEnumsFileNotFound              = errors.New("enums file does not exist")
	errIDsFileNotFound                = errors.New("ids file does not exist")
	errNoProtoFilesConfigured         = errors.New("no proto files configured")
	errInvalidIDsConfig               = errors.New("ids config must be a sequence or mapping")
	errOneOfWithoutDiscriminator      = errors.New("oneof message requires a string or enum discriminator field")
	errOneOfVariantWithoutEnumValue   = errors.New("oneof variant has no matching enum discriminator value")
)

type GeneratorFunc func(ctx context.Context) error

type Generator string

const (
	SwaggerGenerator Generator = "swagger"
	ApiGenerator     Generator = "api"
	EnumsGenerator   Generator = "enums"
	IdsGenerator     Generator = "ids"
	DaoGenerator     Generator = "dao"
)

const (
	importContext = "context"
	importTime    = "time"

	protoTypeBool   = "bool"
	protoTypeDouble = "double"
	protoTypeFloat  = "float"
	protoTypeInt32  = "int32"
	protoTypeInt64  = "int64"
	protoTypeString = "string"
	protoTypeUint32 = "uint32"
	protoTypeUint64 = "uint64"
	protoTypeUUID   = "uuid"

	apiFormatUnixTime = "unix_time"

	sqlTypeInt = "INT"

	// discriminatorFieldName is the conventional sibling field that turns a oneof into an
	// OpenAPI discriminated union: when a message carries a oneof and a field named "type" that is
	// a string or an enum, the value is folded into each variant as a const and a discriminator is
	// emitted
	discriminatorFieldName = "type"

	// oneOfVariantInfix separates the parent schema name from the variant name when naming the
	// synthesized per-variant schemas of a discriminated oneof
	oneOfVariantInfix = "_oneof_"
)

var AllGenerators = []Generator{
	SwaggerGenerator,
	ApiGenerator,
	EnumsGenerator,
	IdsGenerator,
	DaoGenerator,
}

type Gen interface {
	Generate(ctx context.Context, types ...Generator) error
}

type Impl struct {
	log           logger.Loggable
	config        *config.Config
	pathSeparator string
	protoParser   proto_parser.Parser
	storage       storage.Storage
}

type protoSource struct {
	Path    string
	Package string
}

type protoData struct {
	Results *proto_parser.Results
}

type protoIndex struct {
	modelsByAPIName map[string]proto_parser.Model
	modelsByKey     map[string]proto_parser.Model
	enumsByName     map[string]proto_parser.Enum
}

type propertyExtras struct {
	Title       string
	Description string
	Format      string
}

func New(cfg *config.Config) (*Impl, error) {
	pathSeparator := "__"

	workDir := cfg.Gen.Settings.WorkDir
	if workDir == "" {
		workDir = "."
	}

	workStorage := storage.NewStorage(
		providers.NewOsProvider(workDir),
		providers.NoUrlProviderImpl,
	)

	log := logger.NewLoggableImplWithService("gen")

	log.GetLoggerWithoutContext().Debugf("Work dir: %s", workDir)

	return &Impl{
		log:           log,
		config:        cfg,
		pathSeparator: pathSeparator,
		protoParser:   proto_parser.New(pathSeparator),
		storage:       workStorage,
	}, nil
}

func (g *Impl) Generate(ctx context.Context, types ...Generator) error {
	log := g.log.GetLogger(ctx)

	if len(types) == 0 {
		types = AllGenerators
	}

	generators := map[Generator]GeneratorFunc{
		SwaggerGenerator: g.generateSwagger,
		ApiGenerator:     g.generateApi,
		EnumsGenerator:   g.generateEnums,
		IdsGenerator:     g.generateIds,
		DaoGenerator:     g.generateDao,
	}

	for _, generatorType := range types {
		generatorFunc, ok := generators[generatorType]
		if !ok {
			return fmt.Errorf("%w: %s", ErrGeneratorNotFount, generatorType)
		}

		log.Debugf("Generating %q", generatorType)

		tracker := timetrack.New(ctx)

		if err := generatorFunc(ctx); err != nil {
			return fmt.Errorf("generator %q failed: %w", generatorType, err)
		}

		duration := tracker.Finish()
		log.Infof("Generator %q finished in %s", generatorType, duration)
	}

	return nil
}

func (g *Impl) generateSwagger(ctx context.Context) error {
	apiSchema, ok, err := g.loadApiSchema(ctx)
	if err != nil {
		return err
	}

	if !ok {
		return nil
	}

	protoData, err := g.parseProto(ctx, true)
	if err != nil {
		return err
	}

	spec, err := g.buildSwagger(apiSchema, protoData.Results)
	if err != nil {
		return err
	}

	specBuf, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal swagger: %w", err)
	}

	return g.write(ctx, g.swaggerPath(), specBuf)
}

func (g *Impl) generateApi(ctx context.Context) error {
	apiSchema, ok, err := g.loadApiSchema(ctx)
	if err != nil {
		return err
	}

	if !ok {
		return nil
	}

	if err = g.generateApiControllers(ctx, apiSchema); err != nil {
		return err
	}

	if err = g.generateApiRequestHandler(ctx, apiSchema); err != nil {
		return err
	}

	return nil
}

func (g *Impl) generateEnums(ctx context.Context) error {
	enums, ok, err := g.loadEnums(ctx)
	if err != nil {
		return err
	}

	if !ok {
		return nil
	}

	entries := make([]gentemplate.EnumDefinition, 0, len(enums.Enums))
	sqlEntries := make([]gentemplate.EnumSQLDefinition, 0, len(enums.Enums))

	names := make([]string, 0, len(enums.Enums))
	for name := range enums.Enums {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		values := enums.Enums[name]
		enumName := makeNamed(strings.ToLower(name))

		enumValues := make([]gentemplate.EnumGoValue, 0, len(values))
		for _, value := range values {
			valueName := makeNamed(value)
			enumValues = append(enumValues, gentemplate.EnumGoValue{
				ConstName: enumName.CamelCapitalized + valueName.CamelCapitalized,
				Value:     value,
			})
		}

		entries = append(entries, gentemplate.EnumDefinition{
			Name:     name,
			TypeName: enumName.CamelCapitalized,
			Values:   enumValues,
		})

		sqlEntries = append(sqlEntries, gentemplate.EnumSQLDefinition{
			Name:   name,
			Values: values,
		})
	}

	goBuf, err := gentemplate.RenderGo("enums.go.tmpl", gentemplate.EnumGoData{
		File: gentemplate.GoFile{
			Disclaimer: disclaimer,
			Package:    g.enumsPackageName(),
		},
		Enums: entries,
	})
	if err != nil {
		return err
	}

	if err = g.write(ctx, path.Join(g.enumsDir(), "enums_gen.go"), goBuf); err != nil {
		return err
	}

	sqlBuf, err := gentemplate.Render("enums.sql.tmpl", gentemplate.EnumSQLData{
		Disclaimer: disclaimer,
		Enums:      sqlEntries,
	})
	if err != nil {
		return err
	}

	return g.write(ctx, path.Join(g.enumsMigrationsDir(), "enums_gen.sql"), sqlBuf)
}

func (g *Impl) generateIds(ctx context.Context) error {
	ids, ok, err := g.loadIDs(ctx)
	if err != nil {
		return err
	}

	if !ok {
		return nil
	}

	for _, id := range ids {
		if id.Type != "" && id.Type != protoTypeUUID {
			return fmt.Errorf("%w: %s for %s", errUnsupportedIDType, id.Type, id.Name)
		}

		name := makeNamed(id.Name)

		typeName := name.CamelCapitalized
		if !strings.HasSuffix(typeName, "Id") {
			typeName += "Id"
		}

		fileName := strings.TrimSuffix(name.Snake, "_id") + "_gen.go"

		buf, err := gentemplate.RenderGo("id.go.tmpl", gentemplate.IDData{
			File: gentemplate.GoFile{
				Package: g.idsPackageName(),
			},
			TypeName:  typeName,
			EmptyVar:  "empty" + typeName,
			ParseFunc: "Parse" + typeName,
		})
		if err != nil {
			return err
		}

		if err = g.write(ctx, path.Join(g.idsDir(), fileName), buf); err != nil {
			return err
		}
	}

	return nil
}

func (g *Impl) generateDao(ctx context.Context) error {
	sourceDir := g.daoSourceDir()

	exists, err := g.exists(ctx, sourceDir)
	if err != nil {
		return err
	}

	if !exists {
		return nil
	}

	enums, _, err := g.loadEnums(ctx)
	if err != nil {
		return err
	}

	enumsMap := make(map[string]named)

	if enums != nil {
		for name := range enums.Enums {
			enumsMap[name] = makeNamed(strings.ToLower(name))
		}
	}

	entries, err := g.storage.ReadDir(ctx, sourceDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		basename := entry.Name()
		if strings.HasPrefix(basename, ".") || strings.HasPrefix(basename, "~") || strings.HasSuffix(basename, "~") {
			continue
		}

		if err = g.generateDaoFile(ctx, path.Join(sourceDir, basename), enumsMap); err != nil {
			return err
		}
	}

	return nil
}

//nolint:gocognit,cyclop // This maps route config into template data and keeps branches explicit.
func (g *Impl) generateApiControllers(ctx context.Context, apiSchema *ApiSchema) error {
	requestStructs := make([]gentemplate.RequestStruct, 0)
	methods := make([]gentemplate.ControllerMethod, 0)
	imports := []gentemplate.Import{{Path: importContext}}

	hasUUID := false
	hasTime := false
	hasFile := false
	hasHTTP := false

	for _, routeEntry := range apiSchema.Api.GetRoutes() {
		for _, operationEntry := range routeEntry.Operations {
			operation := operationEntry.Route
			methodName := operation.ID

			paramsName := ""
			fields := make([]gentemplate.GoField, 0)

			if len(operation.Security) > 0 ||
				len(operation.Parameters) > 0 ||
				len(operation.RequestFiles) > 0 ||
				operation.RequestModel != "" ||
				operation.RawBody ||
				operation.RawHeaders {
				paramsName = methodName + "Request"
			}

			if paramsName != "" {
				for _, securityName := range operation.Security {
					security, ok := apiSchema.Api.Security[securityName]
					if !ok {
						return fmt.Errorf("%w: %s", errSecurityNotFound, securityName)
					}

					fields = append(fields, gentemplate.GoField{
						Name: "Security",
						Type: security.Model,
					})
				}

				if operation.RequestModel != "" {
					fields = append(fields, gentemplate.GoField{Name: "Request", Type: "*" + operation.RequestModel})
				}

				if operation.RawBody {
					fields = append(fields, gentemplate.GoField{Name: "RawBody", Type: "[]byte"})
				}

				if operation.RawHeaders {
					fields = append(fields, gentemplate.GoField{Name: "RawHeaders", Type: "map[string]string"})
				}

				for _, fileEntry := range operation.RequestFiles {
					hasFile = true

					fields = append(fields, gentemplate.GoField{
						Name: stringy.New(fileEntry.Name).CamelCase().UcFirst(),
						Type: "*http.File",
					})
				}

				for _, paramEntry := range operation.GetParameters() {
					paramType, err := g.apiParamGoType(paramEntry.Parameter)
					if err != nil {
						return fmt.Errorf("%w: %s in %s", err, paramEntry.Name, operation.ID)
					}

					switch paramEntry.Parameter.Format {
					case protoTypeUUID:
						if paramEntry.Parameter.Model == "" {
							hasUUID = true
						}
					case apiFormatUnixTime:
						hasTime = true
					}

					fields = append(fields, gentemplate.GoField{
						Name: stringy.New(paramEntry.Name).SnakeCase().CamelCase().UcFirst(),
						Type: paramType,
					})
				}

				requestStructs = append(requestStructs, gentemplate.RequestStruct{
					Name:   paramsName,
					Fields: fields,
				})
			}

			responseType := g.controllerResponseType(operation, apiSchema.Api.Errors)
			if operation.IsHTTP {
				hasHTTP = true

				responseType = "http.HttpResponse[" + strings.TrimPrefix(responseType, "*") + "]"
				if responseType == "http.HttpResponse[]" {
					responseType = "http.HttpResponse[any]"
				}
			}

			methods = append(methods, gentemplate.ControllerMethod{
				Name:         methodName,
				ParamsType:   paramsName,
				ResponseType: responseType,
			})
		}
	}

	if hasUUID {
		imports = append(imports, gentemplate.Import{Path: "github.com/google/uuid"})
	}

	if hasTime {
		imports = append(imports, gentemplate.Import{Path: importTime})
	}

	if hasFile || hasHTTP {
		imports = append(imports, gentemplate.Import{Path: "github.com/pixality-inc/golang-core/http"})
	}

	controllerImports, err := convertImports(apiSchema.Api.ControllerImports)
	if err != nil {
		return err
	}

	imports = append(imports, controllerImports...)

	buf, err := gentemplate.RenderGo("controller.go.tmpl", gentemplate.ControllerData{
		File: gentemplate.GoFile{
			Disclaimer: disclaimer,
			Package:    "controllers",
			Imports:    uniqueImports(imports),
		},
		RequestStructs: requestStructs,
		Methods:        methods,
	})
	if err != nil {
		return err
	}

	return g.write(ctx, path.Join(g.apiDir(), "controllers", "controller_gen.go"), buf)
}

//nolint:gocognit // This assembles router handler template data from many route options.
func (g *Impl) generateApiRequestHandler(ctx context.Context, apiSchema *ApiSchema) error {
	imports := []gentemplate.Import{
		{Path: importContext},
		{Path: "slices"},
		{Path: "github.com/valyala/fasthttp"},
	}

	registrations := make([]gentemplate.RouteRegistration, 0)
	handlers := make([]gentemplate.RouteHandler, 0)
	needsFmt := false
	needsStrings := false
	needsLogger := false

	for _, routeEntry := range apiSchema.Api.GetRoutes() {
		for _, operationEntry := range routeEntry.Operations {
			operationType := operationEntry.Operation
			operation := operationEntry.Route

			routerMethod, err := routerMethod(operationType)
			if err != nil {
				return fmt.Errorf("%w: %s for %s", err, operationType, operation.ID)
			}

			handlerName := "handle" + operation.ID
			paramsType := ""

			hasParams := len(operation.Security) > 0 ||
				len(operation.Parameters) > 0 ||
				len(operation.RequestFiles) > 0 ||
				operation.RequestModel != "" ||
				operation.RawBody ||
				operation.RawHeaders
			if hasParams {
				paramsType = operation.ID + "Request"
			}

			registrations = append(registrations, gentemplate.RouteRegistration{
				Method:  routerMethod,
				URL:     routeEntry.URL,
				Handler: handlerName,
			})

			securities := make([]gentemplate.HandlerSecurity, 0, len(operation.Security))
			for _, securityName := range operation.Security {
				security, ok := apiSchema.Api.Security[securityName]
				if !ok {
					return fmt.Errorf("%w: %s", errSecurityNotFound, securityName)
				}

				securities = append(securities, gentemplate.HandlerSecurity{
					FieldName:    "Security",
					Getter:       security.ModelGetter,
					AuthRequired: operation.AuthRequired,
				})
			}

			params := make([]gentemplate.HandlerParam, 0, len(operation.Parameters))
			for _, paramEntry := range operation.GetParameters() {
				param, err := g.apiHandlerParam(paramEntry.Name, paramEntry.Parameter)
				if err != nil {
					return fmt.Errorf("%w: %s in %s", err, paramEntry.Name, operation.ID)
				}

				params = append(params, param)
			}

			if len(params) > 0 {
				needsFmt = true
				needsStrings = true
			}

			if operation.RequestModel != "" || len(operation.RequestFiles) > 0 {
				needsFmt = true
			}

			if len(operation.RequestFiles) > 0 {
				needsLogger = true
			}

			files := make([]gentemplate.HandlerFile, 0, len(operation.RequestFiles))
			for _, fileEntry := range operation.RequestFiles {
				files = append(files, gentemplate.HandlerFile{
					FieldName: stringy.New(fileEntry.Name).CamelCase().UcFirst(),
					FormName:  fileEntry.Name,
				})
			}

			httpType := strings.TrimPrefix(g.controllerResponseType(operation, apiSchema.Api.Errors), "*")
			if httpType == "" {
				httpType = "any"
			}

			handlers = append(handlers, gentemplate.RouteHandler{
				Name:             handlerName,
				ControllerMethod: operation.ID,
				ParamsType:       paramsType,
				HasParams:        hasParams,
				Securities:       securities,
				Params:           params,
				RequestModel:     operation.RequestModel,
				RawBody:          operation.RawBody,
				RawHeaders:       operation.RawHeaders,
				Files:            files,
				IsHTTP:           operation.IsHTTP,
				HTTPType:         httpType,
			})
		}
	}

	if needsFmt {
		imports = append(imports, gentemplate.Import{Path: "fmt"})
	}

	if needsStrings {
		imports = append(imports, gentemplate.Import{Path: "strings"})
	}

	if needsLogger {
		imports = append(imports, gentemplate.Import{Path: "github.com/pixality-inc/golang-core/logger"})
	}

	routerImports, err := convertImports(apiSchema.Api.RouterImports)
	if err != nil {
		return err
	}

	imports = append(imports, routerImports...)

	buf, err := gentemplate.RenderGo("request_handler.go.tmpl", gentemplate.RequestHandlerData{
		File: gentemplate.GoFile{
			Disclaimer: disclaimer,
			Package:    g.apiPackageName(),
			Imports:    uniqueImports(imports),
		},
		Registrations: registrations,
		Handlers:      handlers,
	})
	if err != nil {
		return err
	}

	return g.write(ctx, path.Join(g.apiDir(), "request_handler_gen.go"), buf)
}

func (g *Impl) generateDaoFile(ctx context.Context, filename string, enumsMap map[string]named) error {
	sourceFile, err := g.read(ctx, filename)
	if err != nil {
		return err
	}

	var sourceConfig sourceFileConfig
	if err = yamlv3.Unmarshal(sourceFile, &sourceConfig); err != nil {
		return err
	}

	if sourceConfig.ModelName == "" {
		sourceConfig.ModelName = sourceConfig.Name
	}

	model, err := g.generateModel(sourceConfig, enumsMap)
	if err != nil {
		return err
	}

	daoGenData, daoData, daoSQLData, err := g.daoTemplateData(sourceConfig, model)
	if err != nil {
		return err
	}

	daoGen, err := gentemplate.RenderGo("dao_gen.go.tmpl", daoGenData)
	if err != nil {
		return err
	}

	if err = g.write(ctx, path.Join(g.daoDir(), model.DaoName.Snake+"_gen.go"), daoGen); err != nil {
		return err
	}

	daoFile := path.Join(g.daoDir(), model.DaoName.Snake+".go")

	exists, err := g.exists(ctx, daoFile)
	if err != nil {
		return err
	}

	if !exists {
		daoBuf, err := gentemplate.RenderGo("dao.go.tmpl", daoData)
		if err != nil {
			return err
		}

		if err = g.write(ctx, daoFile, daoBuf); err != nil {
			return err
		}
	}

	sqlBuf, err := gentemplate.Render("dao.sql.tmpl", daoSQLData)
	if err != nil {
		return err
	}

	return g.write(ctx, path.Join(g.daoMigrationsDir(), model.DaoName.Snake+"_gen.sql"), sqlBuf)
}

//nolint:gocognit,gocyclo,cyclop,maintidx // OpenAPI generation mirrors the route/model schema surface.
func (g *Impl) buildSwagger(apiSchema *ApiSchema, results *proto_parser.Results) (*openapi3.T, error) {
	index := g.buildProtoIndex(results)

	spec := &openapi3.T{
		OpenAPI: "3.0.0",
		Info: &openapi3.Info{
			Title:       apiSchema.Api.Info.Title,
			Version:     apiSchema.Api.Info.Version,
			Description: apiSchema.Api.Info.Description,
		},
		Servers: []*openapi3.Server{},
		Tags:    []*openapi3.Tag{},
		Components: &openapi3.Components{
			Schemas:         map[string]*openapi3.SchemaRef{},
			SecuritySchemes: map[string]*openapi3.SecuritySchemeRef{},
		},
	}

	for _, server := range apiSchema.Api.Servers {
		spec.Servers = append(spec.Servers, &openapi3.Server{
			URL:         server.URL,
			Description: server.Description,
		})
	}

	for securityName, security := range apiSchema.Api.Security {
		switch security.Type {
		case ApiSecurityTypeBearer:
			spec.Components.SecuritySchemes[securityName] = &openapi3.SecuritySchemeRef{
				Value: &openapi3.SecurityScheme{
					Type:        "http",
					Scheme:      "bearer",
					Description: "`Authorization: Bearer ...`",
				},
			}
		default:
			return nil, fmt.Errorf("%w: %s", errUnknownSecurityType, security.Type)
		}
	}

	schemas := make(map[string]*openapi3.SchemaRef)
	visiting := make(map[string]bool)

	registerSchema := func(name string, ref *openapi3.SchemaRef) {
		schemas[name] = ref
	}

	var addSchema func(modelName string) (string, error)

	addSchema = func(modelName string) (string, error) {
		if errorModel, ok := apiSchema.Api.Errors[modelName]; ok {
			return addSchema(errorModel)
		}

		schemaName := schemaName(modelName)
		if _, ok := schemas[schemaName]; ok {
			return schemaName, nil
		}

		model, ok := index.modelsByAPIName[modelName]
		if !ok {
			return "", fmt.Errorf("%w: %s", errNoSchemaFound, modelName)
		}

		if visiting[schemaName] {
			return schemaName, nil
		}

		visiting[schemaName] = true
		schemas[schemaName] = objectProperty(schemaName, map[string]*openapi3.SchemaRef{}, nil, propertyExtras{})

		schemaRef, err := g.modelSchema(model, index, addSchema, registerSchema)
		if err != nil {
			return "", err
		}

		schemas[schemaName] = schemaRef
		delete(visiting, schemaName)

		return schemaName, nil
	}

	tags := make(map[string]*openapi3.Tag)
	paths := make(map[string]*openapi3.PathItem)

	for _, routeEntry := range apiSchema.Api.GetRoutes() {
		pathItem := &openapi3.PathItem{}
		hasItems := false

		for _, operationEntry := range routeEntry.Operations {
			routeOperation := operationEntry.Operation

			operation := operationEntry.Route
			if operation.Hidden {
				continue
			}

			hasItems = true

			for _, tag := range operation.Tags {
				if _, has := tags[tag]; !has {
					tags[tag] = &openapi3.Tag{Name: tag}
				}
			}

			pathOperation := &openapi3.Operation{
				Summary:     operation.Title,
				Description: operation.Description,
				Tags:        operation.Tags,
				OperationID: operation.ID,
			}

			if operation.Security != nil {
				if len(operation.Security) > 1 {
					return nil, fmt.Errorf("%w: %s", errOnlyOneSecurityAllowed, operation.ID)
				}

				pathOperation.Security = &openapi3.SecurityRequirements{openapi3.SecurityRequirement{
					operation.Security[0]: []string{},
				}}
			}

			parameters, err := g.openapiParameters(operation)
			if err != nil {
				return nil, err
			}

			pathOperation.Parameters = parameters

			switch {
			case len(operation.RequestFiles) > 0:
				filesMap := make(map[string]*openapi3.SchemaRef, len(operation.RequestFiles))

				required := make([]string, 0, len(operation.RequestFiles))
				for _, fileEntry := range operation.RequestFiles {
					filesMap[fileEntry.Name] = stringProperty(propertyExtras{Format: "binary"})
					required = append(required, fileEntry.Name)
				}

				pathOperation.RequestBody = &openapi3.RequestBodyRef{
					Value: &openapi3.RequestBody{
						Description: "Request File",
						Content: openapi3.Content{
							"multipart/form-data": {
								Schema: objectProperty("", filesMap, required, propertyExtras{}),
							},
						},
					},
				}
			case operation.RequestModel != "":
				requestModelName, err := addSchema(operation.RequestModel)
				if err != nil {
					return nil, err
				}

				pathOperation.RequestBody = &openapi3.RequestBodyRef{
					Value: &openapi3.RequestBody{
						Description: requestModelName,
						Content: openapi3.Content{
							"application/json": {
								Schema: refProperty(requestModelName),
							},
						},
					},
				}
			case operation.RawBody:
				pathOperation.RequestBody = &openapi3.RequestBodyRef{
					Value: &openapi3.RequestBody{Description: "Raw Body"},
				}
			}

			responses, err := g.openapiResponses(operation, apiSchema.Api.Errors, addSchema)
			if err != nil {
				return nil, err
			}

			pathOperation.Responses = responses

			switch routeOperation {
			case ApiRouteOperationGet:
				pathItem.Get = pathOperation
			case ApiRouteOperationPost:
				pathItem.Post = pathOperation
			case ApiRouteOperationPut:
				pathItem.Put = pathOperation
			case ApiRouteOperationPatch:
				pathItem.Patch = pathOperation
			case ApiRouteOperationDelete:
				pathItem.Delete = pathOperation
			default:
				return nil, fmt.Errorf("%w: %s", errUnknownRouteOperation, routeOperation)
			}
		}

		if hasItems {
			paths[routeEntry.URL] = pathItem
		}
	}

	spec.Components.Schemas = schemas
	spec.Tags = sortedTags(tags)

	pathOptions := make([]openapi3.NewPathsOption, 0, len(paths))

	pathNames := make([]string, 0, len(paths))
	for pathName := range paths {
		pathNames = append(pathNames, pathName)
	}

	sort.Strings(pathNames)

	for _, pathName := range pathNames {
		pathOptions = append(pathOptions, openapi3.WithPath(pathName, paths[pathName]))
	}

	spec.Paths = openapi3.NewPaths(pathOptions...)

	return spec, nil
}

// oneOfDiscriminator describes the discriminator of a discriminated oneof: the sibling field name
// and, when that field is enum typed, the resolved enum whose value names tag the variants. enum is
// nil for a plain string discriminator
type oneOfDiscriminator struct {
	fieldName string
	enum      proto_parser.Enum
}

// oneOfDiscriminatorField reports whether model carries a protojson-style discriminator: a oneof
// alongside a sibling field named "type" that is either a string or an enum. That field is mandatory
// for any oneof message: modelSchema turns its absence into a generation error, never a silently
// untagged union
func (g *Impl) oneOfDiscriminatorField(model proto_parser.Model, index protoIndex) (oneOfDiscriminator, bool) {
	hasOneOf := false
	found := false

	var discriminator oneOfDiscriminator

	for _, field := range model.Fields() {
		if field.IsOneOf() {
			hasOneOf = true

			continue
		}

		if field.Name() != discriminatorFieldName || field.IsRepeated() || field.IsMap() {
			continue
		}

		if normalizeProtoType(field.Type()) == protoTypeString {
			discriminator = oneOfDiscriminator{fieldName: discriminatorFieldName}
			found = true

			continue
		}

		if enum, ok := g.resolveEnum(model, field.Type(), index); ok {
			discriminator = oneOfDiscriminator{fieldName: discriminatorFieldName, enum: enum}
			found = true
		}
	}

	if hasOneOf && found {
		return discriminator, true
	}

	return oneOfDiscriminator{}, false
}

// stringConstProperty is the discriminator value folded into a variant: a single-value string enum
func stringConstProperty(value string) *openapi3.SchemaRef {
	return &openapi3.SchemaRef{
		Value: &openapi3.Schema{
			Type: &openapi3.Types{openapi3.TypeString},
			Enum: []any{value},
		},
	}
}

// enumValuePrefix derives the conventional protobuf enum value prefix from an enum type name, e.g.
// AssetMetadataType -> ASSET_METADATA_TYPE_
func enumValuePrefix(enumName string) string {
	return stringy.New(enumName).SnakeCase().ToUpper() + "_"
}

// discriminatorValue returns the wire value tagging a oneof variant. For a string discriminator it
// is the variant name. For an enum discriminator it is the enum value name whose normalized form
// (enum prefix stripped, lowercased) equals the variant name
func discriminatorValue(discriminator oneOfDiscriminator, variantName string) (string, error) {
	if discriminator.enum == nil {
		return variantName, nil
	}

	prefix := enumValuePrefix(discriminator.enum.Name())
	for _, entry := range discriminator.enum.Entries() {
		if strings.ToLower(strings.TrimPrefix(entry.Name(), prefix)) == variantName {
			return entry.Name(), nil
		}
	}

	return "", fmt.Errorf("%w: variant %q in enum %q",
		errOneOfVariantWithoutEnumValue, variantName, discriminator.enum.Name())
}

func (g *Impl) modelSchema(
	model proto_parser.Model,
	index protoIndex,
	addSchema func(modelName string) (string, error),
	registerSchema func(name string, ref *openapi3.SchemaRef),
) (*openapi3.SchemaRef, error) {
	objectProperties := make(map[string]*openapi3.SchemaRef)
	requiredProperties := make([]string, 0)

	disc, hasDiscriminator := g.oneOfDiscriminatorField(model, index)
	discriminatorName := disc.fieldName

	// classify fields once so the top-level decision is order-independent: a top-level oneOf is
	// only possible when the sole content is one oneof (plus the folded discriminator, if any)
	oneOfFieldCount := 0
	plainFieldCount := 0

	for _, field := range model.Fields() {
		switch {
		case field.IsOneOf():
			oneOfFieldCount++
		case hasDiscriminator && field.Name() == discriminatorName:
			// reserved discriminator, folded into each variant as a const
		default:
			plainFieldCount++
		}
	}

	// every oneof must carry a string discriminator field; a oneof without one is a generation
	// error rather than a silently-untagged union, so the frontend always gets a tagged union
	if oneOfFieldCount > 0 && !hasDiscriminator {
		return nil, fmt.Errorf("%w: message %q has a oneof but no string or enum field %q",
			errOneOfWithoutDiscriminator, g.apiModelName(model), discriminatorFieldName)
	}

	topLevelOneOf := oneOfFieldCount == 1 && plainFieldCount == 0
	applyDiscriminator := hasDiscriminator && topLevelOneOf

	var topLevelOneOfRefs []*openapi3.SchemaRef

	var discriminator *openapi3.Discriminator

	for _, field := range model.Fields() {
		if applyDiscriminator && !field.IsOneOf() && field.Name() == discriminatorName {
			continue
		}

		if field.IsOneOf() {
			children := field.Children()
			sort.Slice(children, func(i, j int) bool {
				return children[i].Name() < children[j].Name()
			})

			schemaRefs := make([]*openapi3.SchemaRef, 0, len(children))
			mapping := make(openapi3.StringMap[openapi3.MappingRef], len(children))

			for _, child := range children {
				property, _, err := g.fieldSchema(model, child, index, addSchema)
				if err != nil {
					return nil, err
				}

				variant := &openapi3.Schema{
					Type:        &openapi3.Types{openapi3.TypeObject},
					Title:       child.Name(),
					Description: child.Name(),
					Properties: map[string]*openapi3.SchemaRef{
						child.Name(): property,
					},
				}

				if !applyDiscriminator {
					schemaRefs = append(schemaRefs, &openapi3.SchemaRef{Value: variant})

					continue
				}

				// discriminated oneof: fold the type const into the variant, register it as a
				// named schema and reference it. A named ref plus a full mapping is required by
				// clients that reject a discriminator over inline schemas (e.g. oapi-codegen)
				discValue, err := discriminatorValue(disc, child.Name())
				if err != nil {
					return nil, err
				}

				variant.Properties[discriminatorName] = stringConstProperty(discValue)
				variant.Required = []string{discriminatorName, child.Name()}

				variantName := schemaName(g.apiModelName(model)) + oneOfVariantInfix + child.Name()
				registerSchema(variantName, &openapi3.SchemaRef{Value: variant})

				schemaRefs = append(schemaRefs, refProperty(variantName))
				mapping[discValue] = openapi3.MappingRef{Ref: "#/components/schemas/" + variantName}
			}

			if applyDiscriminator {
				discriminator = &openapi3.Discriminator{
					PropertyName: discriminatorName,
					Mapping:      mapping,
				}
			}

			if topLevelOneOf {
				topLevelOneOfRefs = schemaRefs
			} else {
				objectProperties[field.Name()] = &openapi3.SchemaRef{
					Value: &openapi3.Schema{OneOf: schemaRefs},
				}
			}

			continue
		}

		property, required, err := g.fieldSchema(model, field, index, addSchema)
		if err != nil {
			return nil, err
		}

		objectProperties[field.Name()] = property
		if required {
			requiredProperties = append(requiredProperties, field.Name())
		}
	}

	if topLevelOneOf {
		return &openapi3.SchemaRef{
			Value: &openapi3.Schema{OneOf: topLevelOneOfRefs, Discriminator: discriminator},
		}, nil
	}

	return objectProperty(schemaName(g.apiModelName(model)), objectProperties, requiredProperties, propertyExtras{}), nil
}

func (g *Impl) fieldSchema(
	current proto_parser.Model,
	field proto_parser.Field,
	index protoIndex,
	addSchema func(modelName string) (string, error),
) (*openapi3.SchemaRef, bool, error) {
	extras := fieldExtras(field)

	property, err := g.fieldBaseSchema(current, field, index, addSchema, extras)
	if err != nil {
		return nil, false, err
	}

	if field.IsMap() {
		property = &openapi3.SchemaRef{
			Value: &openapi3.Schema{
				Type: &openapi3.Types{openapi3.TypeObject},
				AdditionalProperties: openapi3.AdditionalProperties{
					Schema: property,
				},
			},
		}
		applyExtras(property.Value, extras)
	} else if field.IsRepeated() {
		property = arrayProperty(property, extras)
	}

	return property, field.IsRepeated() || !field.IsOptional(), nil
}

func (g *Impl) fieldBaseSchema(
	current proto_parser.Model,
	field proto_parser.Field,
	index protoIndex,
	addSchema func(modelName string) (string, error),
	extras propertyExtras,
) (*openapi3.SchemaRef, error) {
	switch normalizeProtoType(field.Type()) {
	case protoTypeString:
		return stringProperty(extras), nil
	case protoTypeBool:
		return booleanProperty(extras), nil
	case "bytes":
		extras.Format = coalesce(extras.Format, "binary")

		return stringProperty(extras), nil
	case protoTypeFloat, protoTypeDouble:
		return numberProperty(extras), nil
	case protoTypeInt32, protoTypeUint32, "sint32", "fixed32", "sfixed32":
		extras.Format = coalesce(extras.Format, protoTypeInt32)

		return integerProperty(extras), nil
	case protoTypeInt64, protoTypeUint64, "sint64", "fixed64", "sfixed64":
		extras.Format = coalesce(extras.Format, protoTypeInt64)

		return integerProperty(extras), nil
	}

	if enum, ok := g.resolveEnum(current, field.Type(), index); ok {
		return enumProperty(enum, extras), nil
	}

	model, ok := g.resolveModel(current, field.Type(), index)
	if !ok {
		return nil, fmt.Errorf("%w: %s.%s %s", errUnknownFieldType, g.apiModelName(current), field.Name(), field.Type())
	}

	refName, err := addSchema(g.apiModelName(model))
	if err != nil {
		return nil, err
	}

	return refProperty(refName), nil
}

func (g *Impl) openapiParameters(operation ApiRoute) ([]*openapi3.ParameterRef, error) {
	parameters := make([]*openapi3.ParameterRef, 0, len(operation.Parameters))

	for _, paramEntry := range operation.GetParameters() {
		paramName := paramEntry.Name
		param := paramEntry.Parameter

		schemaType, err := openapiParamType(param)
		if err != nil {
			return nil, fmt.Errorf("%w: %s for %s in %s", err, param.Type, paramName, operation.ID)
		}

		schema := &openapi3.SchemaRef{
			Value: &openapi3.Schema{
				Type:   &openapi3.Types{schemaType},
				Format: param.Format,
			},
		}

		if len(param.Enum) > 0 {
			enumValues := make([]any, 0, len(param.Enum))
			for _, enumValue := range param.Enum {
				enumValues = append(enumValues, enumValue.Value)
			}

			schema.Value.Enum = enumValues
		}

		if param.Array {
			schema = arrayProperty(schema, propertyExtras{})
		}

		parameters = append(parameters, &openapi3.ParameterRef{
			Value: &openapi3.Parameter{
				Name:        paramName,
				In:          string(param.In),
				Description: param.Description,
				Required:    param.Required,
				Schema:      schema,
			},
		})
	}

	return parameters, nil
}

func (g *Impl) openapiResponses(
	operation ApiRoute,
	errorsMap map[string]string,
	addSchema func(modelName string) (string, error),
) (*openapi3.Responses, error) {
	responseOptions := make([]openapi3.NewResponsesOption, 0, len(operation.ResponseModels))

	for _, responseModel := range operation.ResponseModels {
		responseModelName, err := addSchema(responseModel)
		if err != nil {
			return nil, err
		}

		description := responseModelName
		responseRef := &openapi3.ResponseRef{
			Value: &openapi3.Response{
				Description: &description,
				Content: openapi3.Content{
					"application/json": {
						Schema: refProperty(responseModelName),
					},
				},
			},
		}

		if _, ok := errorsMap[responseModel]; ok {
			statusCode, err := strconv.Atoi(responseModel)
			if err != nil {
				return nil, err
			}

			responseOptions = append(responseOptions, openapi3.WithStatus(statusCode, responseRef))
		} else {
			responseOptions = append(responseOptions, openapi3.WithStatus(200, responseRef))
		}
	}

	if len(responseOptions) == 0 {
		description := "No content"
		responseOptions = append(responseOptions, openapi3.WithStatus(204, &openapi3.ResponseRef{
			Value: &openapi3.Response{Description: &description},
		}))
	}

	return openapi3.NewResponses(responseOptions...), nil
}

func (g *Impl) generateModel(sourceConfig sourceFileConfig, enumsMap map[string]named) (*entityModel, error) {
	entityFields := make([]*entityField, 0, len(sourceConfig.Fields))
	attributesMap := make(map[fieldAttribute]bool)
	modelName := makeNamed(sourceConfig.ModelName)
	daoName := makeNamed(sourceConfig.Name)

	model := &entityModel{
		ModelName: modelName,
		DaoName:   daoName,
	}

	for _, field := range sourceConfig.Fields {
		resultField, err := g.generateModelField(field, enumsMap)
		if err != nil {
			return nil, fmt.Errorf("generate model field %s: %w", field.Name, err)
		}

		for _, attr := range resultField.Attributes {
			attributesMap[attr] = true
		}

		if resultField.Primary && model.PrimaryField == nil {
			model.PrimaryField = resultField
		}

		entityFields = append(entityFields, resultField)
	}

	if model.PrimaryField == nil {
		for _, field := range entityFields {
			if field.Name.Snake == "id" {
				model.PrimaryField = field

				break
			}
		}
	}

	attributes := make([]fieldAttribute, 0, len(attributesMap))
	for attr := range attributesMap {
		attributes = append(attributes, attr)
	}

	model.Fields = entityFields
	model.Attributes = attributes

	return model, nil
}

//nolint:gocognit,gocyclo,cyclop,maintidx // DAO field generation is an explicit type mapping table.
func (g *Impl) generateModelField(field sourceFileField, enumsMap map[string]named) (*entityField, error) {
	fieldName := makeNamed(field.Name)

	attributes := make([]fieldAttribute, 0)
	if field.Seq {
		attributes = append(attributes, fieldAttributeSequence)
	}

	if field.Unique {
		attributes = append(attributes, fieldAttributeUnique)
	}

	if field.Nullable {
		attributes = append(attributes, fieldAttributeNullable)
	}

	nullableDataTypeOrDefault := func(def string) string {
		if field.DataType != "" {
			return "*" + field.DataType
		}

		return def
	}

	dataTypeOrDefault := func(def string) string {
		if field.DataType != "" {
			return field.DataType
		}

		return def
	}

	var (
		modelDataType string
		sqlDataType   string
	)

	switch field.Type {
	case protoTypeInt64:
		sqlDataType = "BIGINT"

		modelDataType = dataTypeOrDefault(protoTypeInt64)
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*int64")
		}
	case protoTypeInt32:
		sqlDataType = sqlTypeInt

		modelDataType = dataTypeOrDefault("int")
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*int")
		}
	case protoTypeUint64:
		sqlDataType = sqlTypeInt

		modelDataType = dataTypeOrDefault(protoTypeUint64)
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*uint64")
		}
	case protoTypeUint32:
		sqlDataType = sqlTypeInt

		modelDataType = dataTypeOrDefault("uint32")
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*uint32")
		}
	case protoTypeString, "text":
		sqlDataType = "TEXT"

		modelDataType = dataTypeOrDefault(protoTypeString)
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*string")
		}
	case protoTypeBool:
		sqlDataType = "BOOLEAN"

		modelDataType = dataTypeOrDefault(protoTypeBool)
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*bool")
		}
	case protoTypeUUID:
		attributes = append(attributes, fieldAttributeUUID)
		sqlDataType = "UUID"

		modelDataType = dataTypeOrDefault("uuid.UUID")
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*" + modelDataType)
		}
	case "json":
		if field.DataType == "" {
			return nil, fmt.Errorf("%w: %s", errInvalidJSONField, field.Name)
		}

		attributes = append(attributes, fieldAttributeJSON)
		sqlDataType = "JSONB"

		modelDataType = "postgres.Json[" + field.DataType + "]"
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*" + modelDataType)
		}
	case "date":
		attributes = append(attributes, fieldAttributeTime)
		sqlDataType = "DATE"

		modelDataType = dataTypeOrDefault("time.Time")
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*time.Time")
		}
	case importTime:
		attributes = append(attributes, fieldAttributeTime)
		sqlDataType = "TIME"

		modelDataType = dataTypeOrDefault("time.Time")
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*time.Time")
		}
	case "duration":
		attributes = append(attributes, fieldAttributeTime)
		sqlDataType = "INTERVAL"

		modelDataType = dataTypeOrDefault("time.Duration")
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*time.Duration")
		}
	case "timestamp":
		attributes = append(attributes, fieldAttributeTime)
		sqlDataType = "TIMESTAMPTZ"

		modelDataType = dataTypeOrDefault("time.Time")
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*time.Time")
		}
	case "enum":
		if field.Enum == "" {
			return nil, fmt.Errorf("%w: %s", errInvalidEnumField, field.Name)
		}

		enum, ok := enumsMap[field.Enum]
		if !ok {
			return nil, fmt.Errorf("%w: %s", errEnumNotFound, field.Enum)
		}

		sqlDataType = strings.ToUpper(enum.Snake)

		modelDataType = dataTypeOrDefault(enum.CamelCapitalized)
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("*" + modelDataType)
		}
	case "geometry":
		attributes = append(attributes, fieldAttributeGeometry)
		sqlDataType = "GEOMETRY"

		modelDataType = dataTypeOrDefault("postgres.Geometry")
		if field.Nullable {
			modelDataType = nullableDataTypeOrDefault("sql.Null[" + modelDataType + "]")
		}
	default:
		if strings.HasPrefix(field.Type, "varchar(") && strings.HasSuffix(field.Type, ")") {
			lengthStr := strings.TrimSuffix(strings.TrimPrefix(field.Type, "varchar("), ")")

			length, err := strconv.Atoi(lengthStr)
			if err != nil {
				return nil, fmt.Errorf("%w: %s: %w", errInvalidFieldType, field.Type, err)
			}

			sqlDataType = "VARCHAR(" + strconv.Itoa(length) + ")"

			modelDataType = dataTypeOrDefault(protoTypeString)
			if field.Nullable {
				modelDataType = nullableDataTypeOrDefault("*string")
			}
		} else {
			return nil, fmt.Errorf("%w: %s", errUnsupportedFieldType, field.Type)
		}
	}

	if field.Array {
		if field.ArraySize != 0 {
			arraySize := strconv.Itoa(field.ArraySize)
			sqlDataType += "[" + arraySize + "]"
			modelDataType = "[" + arraySize + "]" + modelDataType
		} else {
			sqlDataType += "[]"
			modelDataType = "[]" + modelDataType
		}
	}

	sqlExtras := make([]string, 0)
	if field.Nullable {
		sqlExtras = append(sqlExtras, "NULL")
	} else {
		sqlExtras = append(sqlExtras, "NOT", "NULL")
	}

	if field.Unique {
		sqlExtras = append(sqlExtras, "UNIQUE")
	}

	if field.Primary {
		sqlExtras = append(sqlExtras, "PRIMARY", "KEY")
	}

	if len(field.References) > 0 {
		if len(field.References) != 2 {
			return nil, fmt.Errorf("%w: got %d", errReferencesMustContainTwoValues, len(field.References))
		}

		sqlExtras = append(sqlExtras, "REFERENCES", sqlIdentRef(field.References[0], field.References[1]))
	}

	if field.Default != "" {
		sqlExtras = append(sqlExtras, "DEFAULT", strings.TrimSpace(field.Default))
	}

	quotedName := strconv.Quote(fieldName.Snake)

	return &entityField{
		Name:       fieldName,
		SQLName:    fieldName.Snake,
		SQLType:    sqlDataType,
		SQLExtras:  strings.Join(sqlExtras, " "),
		ModelName:  fieldName.CamelCapitalized,
		ModelType:  modelDataType,
		Tags:       fmt.Sprintf("`json:%s yaml:%s sql:%s`", quotedName, quotedName, quotedName),
		Primary:    field.Primary,
		Attributes: attributes,
	}, nil
}

func (g *Impl) daoTemplateData(
	sourceConfig sourceFileConfig,
	model *entityModel,
) (gentemplate.DaoGenData, gentemplate.DaoData, gentemplate.DaoSQLData, error) {
	fields := make([]gentemplate.DaoField, 0, len(model.Fields))
	for index, field := range model.Fields {
		fields = append(fields, gentemplate.DaoField{
			Name:      field.Name.Original,
			VarName:   field.Name.Camel,
			GoName:    field.ModelName,
			GoType:    field.ModelType,
			Tags:      field.Tags,
			SQLName:   field.SQLName,
			SQLType:   field.SQLType,
			SQLExtras: field.SQLExtras,
			Last:      index == len(model.Fields)-1,
		})
	}

	daoGenImports, err := convertImports(sourceConfig.DaoImports)
	if err != nil {
		return gentemplate.DaoGenData{}, gentemplate.DaoData{}, gentemplate.DaoSQLData{}, err
	}

	daoGenImports = append([]gentemplate.Import{
		{Path: "github.com/pixality-inc/golang-core/postgres"},
		{Path: "github.com/pixality-inc/squirrel"},
	}, daoGenImports...)

	if modelUses(model, "uuid.") {
		daoGenImports = append(daoGenImports, gentemplate.Import{Path: "github.com/google/uuid"})
	}

	if modelUses(model, "time.") {
		daoGenImports = append(daoGenImports, gentemplate.Import{Path: importTime})
	}

	if modelUses(model, "sql.") {
		daoGenImports = append(daoGenImports, gentemplate.Import{Path: "database/sql"})
	}

	getByIDType := protoTypeInt64
	getByIDColumn := strconv.Quote("id")

	if model.PrimaryField != nil {
		getByIDType = model.PrimaryField.ModelType
		getByIDColumn = model.DaoName.CamelCapitalized + "Table." + model.PrimaryField.ModelName
	}

	daoImports := []gentemplate.Import{
		{Path: importContext},
		{Path: "fmt"},
		{Path: "github.com/pixality-inc/golang-core/postgres"},
		{Path: "github.com/pixality-inc/squirrel"},
	}

	sourceImports, err := convertImports(sourceConfig.Imports)
	if err != nil {
		return gentemplate.DaoGenData{}, gentemplate.DaoData{}, gentemplate.DaoSQLData{}, err
	}

	daoImports = append(daoImports, sourceImports...)

	for _, imp := range daoGenImports {
		if importUsedByType(imp, getByIDType) {
			daoImports = append(daoImports, imp)
		}
	}

	sequenceNames := make([]string, 0)
	indexes := make([]gentemplate.DaoSQLIndex, 0)

	for _, field := range model.Fields {
		if hasAttribute(field.Attributes, fieldAttributeSequence) {
			sequenceNames = append(sequenceNames, model.DaoName.Snake+"_"+field.Name.Snake+"_seq")
		}

		if hasAttribute(field.Attributes, fieldAttributeUnique) {
			indexes = append(indexes, gentemplate.DaoSQLIndex{
				Name:  model.DaoName.Snake + "_" + field.Name.Snake + "_idx",
				Table: model.DaoName.Snake,
				Field: field.Name.Snake,
			})
		}
	}

	common := gentemplate.DaoGenData{
		File: gentemplate.GoFile{
			Disclaimer: disclaimer,
			Package:    g.daoPackageName(),
			Imports:    uniqueImports(daoGenImports),
		},
		ModelName:       model.ModelName.CamelCapitalized,
		ModelImplName:   model.ModelName.CamelCapitalized + "Impl",
		GetterName:      model.ModelName.CamelCapitalized + "Getter",
		SetterName:      model.ModelName.CamelCapitalized + "Setter",
		RowName:         model.ModelName.Camel + "Row",
		NewModelName:    "New" + model.ModelName.CamelCapitalized,
		ConvertFuncName: "convert" + model.ModelName.CamelCapitalized + "RowToModel",
		DaoName:         model.DaoName.DaoSuffix,
		DaoImplName:     model.DaoName.DaoSuffix + "Impl",
		TableColumns:    model.DaoName.CamelCapitalized + "TableColumns",
		TableNameVar:    model.DaoName.CamelCapitalized + "TableName",
		TableName:       model.DaoName.Snake,
		TableVar:        model.DaoName.CamelCapitalized + "Table",
		ColumnsVar:      model.DaoName.ColumnsSuffix,
		Fields:          fields,
	}

	daoData := gentemplate.DaoData{
		File: gentemplate.GoFile{
			Package: g.daoPackageName(),
			Imports: uniqueImports(daoImports),
		},
		ModelName:       common.ModelName,
		GetterName:      common.GetterName,
		DaoName:         common.DaoName,
		DaoImplName:     common.DaoImplName,
		RowName:         common.RowName,
		ConvertFuncName: common.ConvertFuncName,
		GetByIDType:     getByIDType,
		GetByIDColumn:   getByIDColumn,
	}

	sqlData := gentemplate.DaoSQLData{
		Disclaimer: disclaimer,
		TableName:  model.DaoName.Snake,
		Fields:     fields,
		Sequences:  sequenceNames,
		Indexes:    indexes,
	}

	return common, daoData, sqlData, nil
}

func (g *Impl) loadApiSchema(ctx context.Context) (*ApiSchema, bool, error) {
	filename := g.apiSchemaPath()

	exists, err := g.exists(ctx, filename)
	if err != nil {
		return nil, false, err
	}

	if !exists {
		if g.config.Gen.Api.SchemaFile != "" || len(g.config.Gen.Api.ProtoFiles) > 0 || len(g.config.Gen.Api.ProtoSources) > 0 {
			return nil, false, fmt.Errorf("%w: %s", errAPISchemaFileNotFound, filename)
		}

		return nil, false, nil
	}

	buf, err := g.read(ctx, filename)
	if err != nil {
		return nil, false, err
	}

	var apiSchema ApiSchema
	if err = yamlv3.Unmarshal(buf, &apiSchema); err != nil {
		return nil, false, err
	}

	return &apiSchema, true, nil
}

func (g *Impl) loadEnums(ctx context.Context) (*enumsObject, bool, error) {
	filename := g.enumsFilePath()

	exists, err := g.exists(ctx, filename)
	if err != nil {
		return nil, false, err
	}

	if !exists {
		if g.config.Gen.Enums.File != "" {
			return nil, false, fmt.Errorf("%w: %s", errEnumsFileNotFound, filename)
		}

		return nil, false, nil
	}

	buf, err := g.read(ctx, filename)
	if err != nil {
		return nil, false, err
	}

	enums := &enumsObject{Enums: make(map[string][]string)}
	if len(bytes.TrimSpace(buf)) == 0 {
		return enums, true, nil
	}

	if err = yamlv3.Unmarshal(buf, enums); err != nil {
		return nil, false, err
	}

	if enums.Enums == nil {
		enums.Enums = make(map[string][]string)
	}

	return enums, true, nil
}

func (g *Impl) loadIDs(ctx context.Context) ([]idConfig, bool, error) {
	filename, explicit, ok, err := g.idsFilePath(ctx)
	if err != nil {
		return nil, false, err
	}

	if !ok {
		if explicit {
			return nil, false, fmt.Errorf("%w: %s", errIDsFileNotFound, filename)
		}

		return nil, false, nil
	}

	buf, err := g.read(ctx, filename)
	if err != nil {
		return nil, false, err
	}

	if len(bytes.TrimSpace(buf)) == 0 {
		return nil, true, nil
	}

	ids, err := parseIDsConfig(buf)
	if err != nil {
		return nil, false, err
	}

	return ids, true, nil
}

func (g *Impl) parseProto(ctx context.Context, required bool) (*protoData, error) {
	sources, err := g.protoSources(ctx)
	if err != nil {
		return nil, err
	}

	if len(sources) == 0 {
		if required {
			return nil, errors.Join(ErrProtoParse, errNoProtoFilesConfigured)
		}

		return nil, nil
	}

	inputs := make([]proto_parser.Input, 0, len(sources))
	for _, source := range sources {
		buf, err := g.read(ctx, source.Path)
		if err != nil {
			return nil, errors.Join(ErrProtoParse, err)
		}

		pkg := source.Package
		if pkg == "" {
			pkg = inferProtoGoPackage(buf)
		}

		inputs = append(inputs, proto_parser.NewBytesInput(path.Base(source.Path), buf, pkg))
	}

	results, err := g.protoParser.Parse(ctx, inputs)
	if err != nil {
		return nil, errors.Join(ErrProtoParse, err)
	}

	return &protoData{Results: results}, nil
}

func (g *Impl) protoSources(ctx context.Context) ([]protoSource, error) {
	sources := make([]protoSource, 0)

	for _, source := range g.config.Gen.Api.ProtoSources {
		if source.Path == "" {
			continue
		}

		sources = append(sources, protoSource{
			Path:    source.Path,
			Package: source.Package,
		})
	}

	for _, filename := range g.config.Gen.Api.ProtoFiles {
		if filename == "" {
			continue
		}

		sources = append(sources, protoSource{Path: filename})
	}

	if len(sources) == 0 {
		exists, err := g.exists(ctx, "protocol.proto")
		if err != nil {
			return nil, err
		}

		if exists {
			sources = append(sources, protoSource{Path: "protocol.proto"})
		}
	}

	if g.config.Gen.Api.ModelsPrefix != "" {
		pkg := strings.TrimSuffix(g.config.Gen.Api.ModelsPrefix, ".")

		for index := range sources {
			if sources[index].Package == "" {
				sources[index].Package = pkg
			}
		}
	}

	return sources, nil
}

func (g *Impl) buildProtoIndex(results *proto_parser.Results) protoIndex {
	index := protoIndex{
		modelsByAPIName: make(map[string]proto_parser.Model),
		modelsByKey:     make(map[string]proto_parser.Model),
		enumsByName:     make(map[string]proto_parser.Enum),
	}

	for key, model := range results.Models {
		apiName := g.apiModelName(model)
		index.modelsByAPIName[apiName] = model
		index.modelsByKey[key] = model
		index.modelsByKey[strings.ReplaceAll(key, g.pathSeparator, ".")] = model

		if len(model.Path()) == 0 {
			index.modelsByKey[model.Name()] = model
		}
	}

	for key, enum := range results.Enums {
		index.enumsByName[key] = enum
		index.enumsByName[strings.ReplaceAll(key, g.pathSeparator, ".")] = enum

		if len(enum.Path()) == 0 {
			index.enumsByName[enum.Name()] = enum
		}
	}

	return index
}

func (g *Impl) resolveModel(current proto_parser.Model, typ string, index protoIndex) (proto_parser.Model, bool) {
	for _, candidate := range g.typeCandidates(current, typ) {
		if model, ok := index.modelsByKey[candidate]; ok {
			return model, true
		}
	}

	return nil, false
}

func (g *Impl) resolveEnum(current proto_parser.Model, typ string, index protoIndex) (proto_parser.Enum, bool) {
	for _, candidate := range g.typeCandidates(current, typ) {
		if enum, ok := index.enumsByName[candidate]; ok {
			return enum, true
		}
	}

	return nil, false
}

func (g *Impl) typeCandidates(current proto_parser.Model, typ string) []string {
	raw := strings.TrimSpace(typ)
	absolute := strings.HasPrefix(raw, ".")
	clean := normalizeProtoType(raw)
	clean = strings.TrimPrefix(clean, ".")

	candidates := make([]string, 0)

	if current.Package() != "" {
		pkgPrefix := current.Package() + "."
		if after, ok := strings.CutPrefix(clean, pkgPrefix); ok {
			clean = after
		}
	}

	typePath := strings.ReplaceAll(clean, ".", g.pathSeparator)

	if absolute {
		candidates = append(candidates, typePath)
	} else {
		scope := append(append([]string{}, current.Path()...), current.Name())
		for i := len(scope); i >= 0; i-- {
			base := append([]string{}, scope[:i]...)
			base = append(base, typePath)
			candidates = append(candidates, strings.Join(base, g.pathSeparator))
		}
	}

	candidates = append(candidates, clean, typePath)

	if !absolute && strings.Contains(clean, ".") {
		parts := strings.Split(clean, ".")
		candidates = append(candidates, parts[len(parts)-1])
	}

	return uniqueStrings(candidates)
}

func (g *Impl) apiModelName(model proto_parser.Model) string {
	fullName := proto_parser.GetFullName(model, g.pathSeparator)
	fullName = strings.ReplaceAll(fullName, g.pathSeparator, "_")

	prefix := g.config.Gen.Api.ModelsPrefix
	if prefix == "" && model.Package() != "" {
		prefix = strings.TrimSuffix(model.Package(), ".") + "."
	}

	return prefix + fullName
}

func (g *Impl) apiParamGoType(param ApiRouteParameter) (string, error) {
	var typeStr string

	switch {
	case param.Model != "":
		typeStr = param.Model
	case param.Format != "":
		switch param.Format {
		case protoTypeUint64:
			typeStr = protoTypeUint64
		case protoTypeUUID:
			typeStr = "uuid.UUID"
		case apiFormatUnixTime:
			typeStr = "time.Time"
		default:
			return "", fmt.Errorf("%w: %s", errUnknownParameterFormat, param.Format)
		}
	default:
		switch param.Type {
		case protoTypeString:
			typeStr = protoTypeString
		case protoTypeBool:
			typeStr = protoTypeBool
		case "integer", "int", protoTypeInt64:
			typeStr = protoTypeInt64
		case "number", protoTypeFloat:
			typeStr = "float64"
		default:
			return "", fmt.Errorf("%w: %s", errUnknownParameterType, param.Type)
		}
	}

	if param.Array {
		typeStr = "[]" + typeStr
	} else if !param.Required {
		typeStr = "*" + typeStr
	}

	return typeStr, nil
}

func (g *Impl) apiHandlerParam(name string, param ApiRouteParameter) (gentemplate.HandlerParam, error) {
	result := gentemplate.HandlerParam{
		FieldName:    stringy.New(name).SnakeCase().CamelCase().UcFirst(),
		SourceName:   name,
		In:           string(param.In),
		Required:     param.Required,
		Array:        param.Array,
		ErrorContext: name,
	}

	switch param.In {
	case ApiRouteParameterInPath, ApiRouteParameterInQuery, ApiRouteParameterInHeader:
	default:
		return result, fmt.Errorf("%w: %s", errUnknownParameterIn, param.In)
	}

	if param.Array && (param.Model != "" || param.Format != "" || param.Type != protoTypeString) {
		return result, fmt.Errorf("%w: %s", errParameterArrayNotSupported, name)
	}

	switch {
	case param.Model != "":
		if param.ModelGetter == "" {
			return result, fmt.Errorf("%w: %s", errNoModelGetterSet, name)
		}

		result.ModelGetter = param.ModelGetter

		return result, nil
	case param.Format != "":
		switch param.Format {
		case protoTypeUUID:
			result.ParseFunc = "http.ParseUUID"
		case apiFormatUnixTime:
			result.ParseFunc = "http.ParseUnixTime"
		case protoTypeUint64:
			result.ParseFunc = "http.ParseUint64"
		default:
			return result, fmt.Errorf("%w: %s", errUnknownParameterFormat, param.Format)
		}

		return result, nil
	default:
		switch param.Type {
		case protoTypeString:
			if param.Array {
				result.ArrayString = true
			} else {
				result.String = true
			}
		case protoTypeBool:
			result.ParseFunc = "http.ParseBool"
		default:
			return result, fmt.Errorf("%w: %s", errUnknownParameterType, param.Type)
		}
	}

	return result, nil
}

func (g *Impl) controllerResponseType(operation ApiRoute, errorsMap map[string]string) string {
	for _, modelName := range operation.ResponseModels {
		if _, ok := errorsMap[modelName]; ok {
			continue
		}

		return "*" + modelName
	}

	return "any"
}

func (g *Impl) apiSchemaPath() string {
	if g.config.Gen.Api.SchemaFile != "" {
		return g.config.Gen.Api.SchemaFile
	}

	return path.Join(g.sourceDir(), "api.yaml")
}

func (g *Impl) swaggerPath() string {
	return path.Join(g.apiDocsDir(), "swagger.yaml")
}

func (g *Impl) sourceDir() string {
	return "gen"
}

func (g *Impl) apiDir() string {
	if g.config.Gen.Api.Dir != "" {
		return g.config.Gen.Api.Dir
	}

	return "internal/api"
}

func (g *Impl) apiDocsDir() string {
	if g.config.Gen.Api.DocsDir != "" {
		return g.config.Gen.Api.DocsDir
	}

	return "docs"
}

func (g *Impl) apiPackageName() string {
	if g.config.Gen.Api.PackageName != "" {
		return g.config.Gen.Api.PackageName
	}

	return path.Base(g.apiDir())
}

func (g *Impl) daoSourceDir() string {
	if g.config.Gen.Dao.SourceDir != "" {
		return g.config.Gen.Dao.SourceDir
	}

	return path.Join(g.sourceDir(), "dao")
}

func (g *Impl) daoDir() string {
	if g.config.Gen.Dao.Dir != "" {
		return g.config.Gen.Dao.Dir
	}

	return "internal/dao"
}

func (g *Impl) daoMigrationsDir() string {
	if g.config.Gen.Dao.MigrationsDir != "" {
		return g.config.Gen.Dao.MigrationsDir
	}

	if g.config.Gen.Enums.MigrationsDir != "" {
		return g.config.Gen.Enums.MigrationsDir
	}

	return "migrations/models"
}

func (g *Impl) daoPackageName() string {
	if g.config.Gen.Dao.PackageName != "" {
		return g.config.Gen.Dao.PackageName
	}

	return path.Base(g.daoDir())
}

func (g *Impl) enumsFilePath() string {
	if g.config.Gen.Enums.File != "" {
		return g.config.Gen.Enums.File
	}

	return path.Join(g.sourceDir(), "enums.yaml")
}

func (g *Impl) enumsDir() string {
	if g.config.Gen.Enums.Dir != "" {
		return g.config.Gen.Enums.Dir
	}

	return g.daoDir()
}

func (g *Impl) enumsMigrationsDir() string {
	if g.config.Gen.Enums.MigrationsDir != "" {
		return g.config.Gen.Enums.MigrationsDir
	}

	return g.daoMigrationsDir()
}

func (g *Impl) enumsPackageName() string {
	if g.config.Gen.Enums.PackageName != "" {
		return g.config.Gen.Enums.PackageName
	}

	return path.Base(g.enumsDir())
}

func (g *Impl) idsFilePath(ctx context.Context) (string, bool, bool, error) {
	if g.config.Gen.Ids.File != "" {
		exists, err := g.exists(ctx, g.config.Gen.Ids.File)

		return g.config.Gen.Ids.File, true, exists, err
	}

	for _, candidate := range []string{path.Join(g.sourceDir(), "ids.yaml"), "ids.yaml"} {
		exists, err := g.exists(ctx, candidate)
		if err != nil {
			return "", false, false, err
		}

		if exists {
			return candidate, false, true, nil
		}
	}

	return path.Join(g.sourceDir(), "ids.yaml"), false, false, nil
}

func (g *Impl) idsDir() string {
	if g.config.Gen.Ids.Dir != "" {
		return g.config.Gen.Ids.Dir
	}

	return "internal/types"
}

func (g *Impl) idsPackageName() string {
	if g.config.Gen.Ids.PackageName != "" {
		return g.config.Gen.Ids.PackageName
	}

	return path.Base(g.idsDir())
}

func (g *Impl) exists(ctx context.Context, filename string) (bool, error) {
	return g.storage.FileExists(ctx, filename)
}

func (g *Impl) read(ctx context.Context, filename string) ([]byte, error) {
	reader, err := g.storage.ReadFile(ctx, filename)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			g.log.GetLogger(ctx).WithError(closeErr).Errorf("failed to close %s", filename)
		}
	}()

	result, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}

	return result, nil
}

func (g *Impl) write(ctx context.Context, filename string, data []byte) error {
	return g.storage.Write(ctx, filename, bytes.NewReader(data))
}

func convertImports(imports [][]string) ([]gentemplate.Import, error) {
	result := make([]gentemplate.Import, 0, len(imports))
	for _, imp := range imports {
		if len(imp) != 2 {
			return nil, fmt.Errorf("%w: %#v", errInvalidImport, imp)
		}

		if imp[1] == "" {
			continue
		}

		result = append(result, gentemplate.Import{
			Alias: imp[0],
			Path:  imp[1],
		})
	}

	return result, nil
}

func uniqueImports(imports []gentemplate.Import) []gentemplate.Import {
	seen := make(map[string]bool)
	result := make([]gentemplate.Import, 0, len(imports))

	for _, imp := range imports {
		key := imp.Alias + "\x00" + imp.Path
		if seen[key] || imp.Path == "" {
			continue
		}

		seen[key] = true

		result = append(result, imp)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	return result
}

func enumEntriesSortedByValue(entries []proto_parser.EnumEntry) []proto_parser.EnumEntry {
	result := append([]proto_parser.EnumEntry{}, entries...)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Value() < result[j].Value()
	})

	return result
}

func sortedTags(tags map[string]*openapi3.Tag) []*openapi3.Tag {
	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}

	sort.Strings(names)

	result := make([]*openapi3.Tag, 0, len(tags))
	for _, name := range names {
		result = append(result, tags[name])
	}

	return result
}

func routerMethod(operation ApiRouteOperation) (string, error) {
	switch operation {
	case ApiRouteOperationGet:
		return "GET", nil
	case ApiRouteOperationPost:
		return "POST", nil
	case ApiRouteOperationPut:
		return "PUT", nil
	case ApiRouteOperationPatch:
		return "PATCH", nil
	case ApiRouteOperationDelete:
		return "DELETE", nil
	default:
		return "", errUnknownRouteOperation
	}
}

func openapiParamType(param ApiRouteParameter) (string, error) {
	switch param.Type {
	case protoTypeString:
		return openapi3.TypeString, nil
	case protoTypeBool:
		return openapi3.TypeBoolean, nil
	case "integer", "int", protoTypeInt64, protoTypeUint64:
		return openapi3.TypeInteger, nil
	case "number", protoTypeFloat:
		return openapi3.TypeNumber, nil
	default:
		return "", errUnknownParameterType
	}
}

func objectProperty(
	name string,
	properties map[string]*openapi3.SchemaRef,
	required []string,
	extras propertyExtras,
) *openapi3.SchemaRef {
	schema := &openapi3.Schema{
		Type:       &openapi3.Types{openapi3.TypeObject},
		Required:   required,
		Properties: properties,
	}
	if name != "" {
		schema.Description = fmt.Sprintf("[%s](#/schemas/%s)", name, name)
	}

	applyExtras(schema, extras)

	return &openapi3.SchemaRef{Value: schema}
}

func stringProperty(extras propertyExtras) *openapi3.SchemaRef {
	return makeProperty(openapi3.TypeString, extras)
}

func integerProperty(extras propertyExtras) *openapi3.SchemaRef {
	return makeProperty(openapi3.TypeInteger, extras)
}

func numberProperty(extras propertyExtras) *openapi3.SchemaRef {
	return makeProperty(openapi3.TypeNumber, extras)
}

func booleanProperty(extras propertyExtras) *openapi3.SchemaRef {
	return makeProperty(openapi3.TypeBoolean, extras)
}

func makeProperty(propertyType string, extras propertyExtras) *openapi3.SchemaRef {
	schema := &openapi3.Schema{
		Type: &openapi3.Types{propertyType},
	}
	applyExtras(schema, extras)

	return &openapi3.SchemaRef{Value: schema}
}

func arrayProperty(of *openapi3.SchemaRef, extras propertyExtras) *openapi3.SchemaRef {
	schema := &openapi3.Schema{
		Type:  &openapi3.Types{openapi3.TypeArray},
		Items: of,
	}
	applyExtras(schema, extras)

	return &openapi3.SchemaRef{Value: schema}
}

// enumProperty renders a proto enum as a string schema whose allowed values are the enum value
// names. the wire form follows protojson with UseEnumNumbers disabled, where an enum serializes as
// its value name. the description keeps the name=number mapping for readability
func enumProperty(enum proto_parser.Enum, extras propertyExtras) *openapi3.SchemaRef {
	entries := enumEntriesSortedByValue(enum.Entries())
	enumAny := make([]any, 0, len(entries))
	descriptionParts := make([]string, 0, len(entries))

	for _, entry := range entries {
		enumAny = append(enumAny, entry.Name())
		descriptionParts = append(descriptionParts, entry.Name()+" = "+strconv.Itoa(entry.Value()))
	}

	schema := &openapi3.Schema{
		Type:        &openapi3.Types{openapi3.TypeString},
		Enum:        enumAny,
		Description: strings.Join(descriptionParts, "\n"),
	}
	applyExtras(schema, extras)

	return &openapi3.SchemaRef{Value: schema}
}

func refProperty(refName string) *openapi3.SchemaRef {
	return &openapi3.SchemaRef{Ref: "#/components/schemas/" + refName}
}

func applyExtras(schema *openapi3.Schema, extras propertyExtras) {
	if extras.Title != "" {
		schema.Title = extras.Title
	}

	if extras.Description != "" {
		schema.Description = extras.Description
	}

	if extras.Format != "" {
		schema.Format = extras.Format
	}
}

func fieldExtras(field proto_parser.Field) propertyExtras {
	extras := propertyExtras{
		Description: field.Comment(),
	}

	for _, value := range field.Attributes() {
		parsed := parseTagExtras(value)
		if parsed.Title != "" {
			extras.Title = parsed.Title
		}

		if parsed.Description != "" {
			extras.Description = parsed.Description
		}

		if parsed.Format != "" {
			extras.Format = parsed.Format
		}
	}

	return extras
}

var tagExtraRegexp = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*):"([^"]*)"`)

func parseTagExtras(value string) propertyExtras {
	if unquoted, err := strconv.Unquote(value); err == nil {
		value = unquoted
	}

	result := propertyExtras{}

	for _, match := range tagExtraRegexp.FindAllStringSubmatch(value, -1) {
		switch match[1] {
		case "title":
			result.Title = match[2]
		case "description":
			result.Description = match[2]
		case "format":
			result.Format = match[2]
		}
	}

	return result
}

func schemaName(modelName string) string {
	split := strings.Split(modelName, ".")
	modelSlug := split[len(split)-1]

	return stringy.New(modelSlug).SnakeCase().ToLower()
}

func normalizeProtoType(typ string) string {
	return strings.TrimSpace(strings.TrimPrefix(typ, "."))
}

func coalesce(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))

	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}

		seen[value] = true
		result = append(result, value)
	}

	return result
}

func modelUses(model *entityModel, needle string) bool {
	for _, field := range model.Fields {
		if strings.Contains(field.ModelType, needle) {
			return true
		}
	}

	return false
}

func importUsedByType(imp gentemplate.Import, typeName string) bool {
	if imp.Path == "" {
		return false
	}

	name := imp.Alias
	if name == "" {
		name = path.Base(imp.Path)
	}

	return strings.Contains(typeName, name+".")
}

func hasAttribute(attributes []fieldAttribute, attr fieldAttribute) bool {
	return slices.Contains(attributes, attr)
}

func sqlIdentRef(table string, field string) string {
	return `"` + strings.ReplaceAll(table, `"`, `""`) + `"("` + strings.ReplaceAll(field, `"`, `""`) + `")`
}

var (
	goPackageRegexp    = regexp.MustCompile(`(?m)option\s+go_package\s*=\s*"([^"]+)"\s*;`)
	protoPackageRegexp = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z0-9_.]+)\s*;`)
)

func inferProtoGoPackage(source []byte) string {
	if match := goPackageRegexp.FindSubmatch(source); len(match) == 2 {
		value := string(match[1])
		if parts := strings.Split(value, ";"); len(parts) == 2 && parts[1] != "" {
			return parts[1]
		}

		value = strings.TrimRight(value, "/")
		if base := path.Base(value); base != "." && base != "/" && base != "" {
			return base
		}
	}

	if match := protoPackageRegexp.FindSubmatch(source); len(match) == 2 {
		parts := strings.Split(string(match[1]), ".")

		return parts[len(parts)-1]
	}

	return ""
}

func parseIDsConfig(buf []byte) ([]idConfig, error) {
	var node yamlv3.Node
	if err := yamlv3.Unmarshal(buf, &node); err != nil {
		return nil, err
	}

	if len(node.Content) == 0 {
		return nil, nil
	}

	root := node.Content[0]
	if root.Kind == yamlv3.MappingNode {
		for index := 0; index+1 < len(root.Content); index += 2 {
			if root.Content[index].Value == "ids" {
				return parseIDsNode(root.Content[index+1])
			}
		}
	}

	return parseIDsNode(root)
}

func parseIDsNode(node *yamlv3.Node) ([]idConfig, error) {
	switch node.Kind {
	case yamlv3.SequenceNode:
		result := make([]idConfig, 0, len(node.Content))
		for _, item := range node.Content {
			var id idConfig
			if err := item.Decode(&id); err != nil {
				return nil, err
			}

			if id.Name != "" {
				result = append(result, id)
			}
		}

		return result, nil

	case yamlv3.MappingNode:
		result := make([]idConfig, 0, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			id := idConfig{Name: node.Content[index].Value}
			if node.Content[index+1].Kind == yamlv3.MappingNode {
				if err := node.Content[index+1].Decode(&id); err != nil {
					return nil, err
				}

				if id.Name == "" {
					id.Name = node.Content[index].Value
				}
			}

			result = append(result, id)
		}

		return result, nil

	default:
		return nil, errInvalidIDsConfig
	}
}
