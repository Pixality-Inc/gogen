package template

type Import struct {
	Alias string
	Path  string
}

type GoFile struct {
	Disclaimer string
	Package    string
	Imports    []Import
}

type GoField struct {
	Name string
	Type string
}

type RequestStruct struct {
	Name   string
	Fields []GoField
}

type ControllerMethod struct {
	Name         string
	ParamsType   string
	ResponseType string
}

type ControllerData struct {
	File           GoFile
	RequestStructs []RequestStruct
	Methods        []ControllerMethod
}

type RouteRegistration struct {
	Method  string
	URL     string
	Handler string
}

type HandlerSecurity struct {
	FieldName    string
	Getter       string
	AuthRequired bool
}

type HandlerParam struct {
	FieldName    string
	SourceName   string
	In           string
	Required     bool
	Array        bool
	ArrayString  bool
	String       bool
	ParseFunc    string
	ModelGetter  string
	ErrorContext string
}

type HandlerFile struct {
	FieldName string
	FormName  string
}

type RouteHandler struct {
	Name             string
	ControllerMethod string
	ParamsType       string
	HasParams        bool
	Securities       []HandlerSecurity
	Params           []HandlerParam
	RequestModel     string
	RawBody          bool
	RawHeaders       bool
	Files            []HandlerFile
	IsHTTP           bool
	HTTPType         string
}

type RequestHandlerData struct {
	File          GoFile
	Registrations []RouteRegistration
	Handlers      []RouteHandler
}

type EnumGoData struct {
	File  GoFile
	Enums []EnumDefinition
}

type EnumDefinition struct {
	Name     string
	TypeName string
	Values   []EnumGoValue
}

type EnumGoValue struct {
	ConstName string
	Value     string
}

type EnumSQLData struct {
	Disclaimer string
	Enums      []EnumSQLDefinition
}

type EnumSQLDefinition struct {
	Name   string
	Values []string
}

type DaoField struct {
	Name      string
	VarName   string
	GoName    string
	GoType    string
	Tags      string
	SQLName   string
	SQLType   string
	SQLExtras string
	Last      bool
}

type DaoGenData struct {
	File            GoFile
	ModelName       string
	ModelImplName   string
	GetterName      string
	SetterName      string
	RowName         string
	NewModelName    string
	ConvertFuncName string
	DaoName         string
	DaoImplName     string
	TableColumns    string
	TableNameVar    string
	TableName       string
	TableVar        string
	ColumnsVar      string
	Fields          []DaoField
}

type DaoData struct {
	File            GoFile
	ModelName       string
	GetterName      string
	DaoName         string
	DaoImplName     string
	RowName         string
	ConvertFuncName string
	GetByIDType     string
	GetByIDColumn   string
}

type DaoSQLData struct {
	Disclaimer string
	TableName  string
	Fields     []DaoField
	Sequences  []string
	Indexes    []DaoSQLIndex
}

type DaoSQLIndex struct {
	Name  string
	Table string
	Field string
}

type IDData struct {
	File      GoFile
	TypeName  string
	EmptyVar  string
	ParseFunc string
}
