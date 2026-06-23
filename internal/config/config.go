package config

import (
	"os"
	"path/filepath"
	"runtime"

	coreConfig "github.com/pixality-inc/golang-core/config"
)

type (
	Settings struct {
		WorkDir string `json:"work_dir" yaml:"work_dir"`
	}

	Project struct {
		ID        string `json:"id"        yaml:"id"`
		Namespace string `json:"namespace" yaml:"namespace"`
	}

	ProtoFile struct {
		Path    string `json:"path"    yaml:"path"`
		Package string `json:"package" yaml:"package"`
	}

	Api struct {
		ProtoFiles   []string    `json:"proto_files"   yaml:"proto_files"`
		ProtoSources []ProtoFile `json:"proto_sources" yaml:"proto_sources"`
		SchemaFile   string      `json:"schema_file"   yaml:"schema_file"`
		Dir          string      `json:"dir"           yaml:"dir"`
		DocsDir      string      `json:"docs_dir"      yaml:"docs_dir"`
		PackageName  string      `json:"package_name"  yaml:"package_name"`
		ModelsPrefix string      `json:"models_prefix" yaml:"models_prefix"`
	}

	Dao struct {
		Dir           string `json:"dir"            yaml:"dir"`
		SourceDir     string `json:"source_dir"     yaml:"source_dir"`
		MigrationsDir string `json:"migrations_dir" yaml:"migrations_dir"`
		PackageName   string `json:"package_name"   yaml:"package_name"`
	}

	Enums struct {
		File          string `json:"file"           yaml:"file"`
		Dir           string `json:"dir"            yaml:"dir"`
		MigrationsDir string `json:"migrations_dir" yaml:"migrations_dir"`
		PackageName   string `json:"package_name"   yaml:"package_name"`
	}

	Ids struct {
		File        string `json:"file"         yaml:"file"`
		Dir         string `json:"dir"          yaml:"dir"`
		PackageName string `json:"package_name" yaml:"package_name"`
	}

	Gen struct {
		Settings Settings `json:"settings" yaml:"settings"`
		Project  Project  `json:"project"  yaml:"project"`
		Api      Api      `json:"api"      yaml:"api"`
		Dao      Dao      `json:"dao"      yaml:"dao"`
		Enums    Enums    `json:"enums"    yaml:"enums"`
		Ids      Ids      `json:"ids"      yaml:"ids"`
	}
)

type Config struct {
	Gen Gen `json:"gen" yaml:"gen"`
}

func RootDir() string {
	var (
		_, b, _, _ = runtime.Caller(0)
		basepath   = filepath.Join(filepath.Dir(b), "../..")
	)

	return basepath
}

func configFile() string {
	configFilename := os.Getenv("GOGEN_CONFIG_FILE")
	if configFilename == "" {
		configFilename = filepath.Join(RootDir(), "gogen.yaml")
	}

	return configFilename
}

func LoadConfig() *Config {
	cfg, err := coreConfig.NewConfig[Config](configFile())
	if err != nil {
		return &Config{}
	}

	return cfg
}

func LoadConfigFromFile(configFilename string) *Config {
	return coreConfig.LoadConfig[Config](configFilename)
}
