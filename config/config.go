package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"

	"github.com/gobackup/gobackup/helper"
	"github.com/gobackup/gobackup/logger"
)

var (
	// Exist Is config file exist
	Exist bool
	// Models configs
	Models []ModelConfig
	// gobackup base dir
	GoBackupDir string = getGoBackupDir()

	PidFilePath string = filepath.Join(GoBackupDir, "gobackup.pid")
	LogFilePath string = filepath.Join(GoBackupDir, "gobackup.log")
	Web         WebConfig

	wLock = sync.Mutex{}

	// The config file loaded at
	UpdatedAt time.Time

	onConfigChanges = make([]func(fsnotify.Event), 0)
)

type WebConfig struct {
	Host     string
	Port     string
	Username string
	Password string
}

type ScheduleConfig struct {
	Enabled bool `json:"enabled,omitempty"`
	// Cron expression
	Cron string `json:"cron,omitempty"`
	// Every
	Every string `json:"every,omitempty"`
	// At time
	At string `json:"at,omitempty"`
}

func (sc ScheduleConfig) String() string {
	if sc.Enabled {
		if len(sc.Cron) > 0 {
			return fmt.Sprintf("cron %s", sc.Cron)
		} else {
			if len(sc.At) > 0 {
				return fmt.Sprintf("every %s at %s", sc.Every, sc.At)
			} else {
				return fmt.Sprintf("every %s", sc.Every)
			}
		}
	}

	return "disabled"
}

// ModelConfig for special case
type ModelConfig struct {
	Name        string
	Description string
	// WorkDir of the gobackup started
	WorkDir        string
	TempPath       string
	DumpPath       string
	Schedule       ScheduleConfig
	CompressWith   SubConfig
	EncryptWith    SubConfig
	Archive        *viper.Viper
	Splitter       *viper.Viper
	Databases      map[string]SubConfig
	Storages       map[string]SubConfig
	DefaultStorage string
	Notifiers      map[string]SubConfig
	Viper          *viper.Viper
	BeforeScript   string
	AfterScript    string
}

func getGoBackupDir() string {
	dir := os.Getenv("GOBACKUP_DIR")
	if len(dir) == 0 {
		dir = filepath.Join(os.Getenv("HOME"), ".gobackup")
	}
	return dir
}

// SubConfig sub config info
type SubConfig struct {
	Name  string
	Type  string
	Viper *viper.Viper
}

// Init
// loadConfig from:
// - ./gobackup.yml
// - ~/.gobackup/gobackup.yml
// - /etc/gobackup/gobackup.yml
func Init(configFile string) error {
	logger := logger.Tag("Config")

	// Log the raw environment variable directly from the OS
	envVar := os.Getenv("MODELS_RDS_DATABASES_DOCKER_DB_DATABASE")
	logger.Info("Raw env var MODELS_RDS_DATABASES_DOCKER_DB_DATABASE:", envVar)
	// set config file directly
	if len(configFile) > 0 {
		configFile = helper.AbsolutePath(configFile)
		logger.Info("Load config:", configFile)

		viper.SetConfigFile(configFile)
		viper.AutomaticEnv()
		viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
		viper.BindEnv("models.rds.databases.docker_db.database", "MODELS_RDS_DATABASES_DOCKER_DB_DATABASE")
		logger.Info("Database from env1: ", viper.GetString("models.rds.databases.docker_db.database"))

	} else {
		logger.Info("Load config from env variables for db username, database, password path.")
		viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
		viper.AutomaticEnv()
		viper.SetConfigName("gobackup")

		// ./gobackup.yml
		viper.AddConfigPath(".")
		// ~/.gobackup/gobackup.yml
		viper.AddConfigPath("$HOME/.gobackup") // call multiple times to add many search paths
		// /etc/gobackup/gobackup.yml
		viper.AddConfigPath("/etc/gobackup/") // path to look for the config file in

		// Log the current Viper settings to see the loaded config
		logger.Info("Loaded Viper settings: ", viper.AllSettings()) // Logs all settings

	}

	viper.WatchConfig()
	viper.OnConfigChange(func(in fsnotify.Event) {
		logger.Info("Config file changed:", in.Name)
		defer onConfigChanged(in)
		if err := loadConfig(); err != nil {
			logger.Error(err.Error())
		}
	})

	return loadConfig()
}

// OnConfigChange add callback when config changed
func OnConfigChange(run func(in fsnotify.Event)) {
	onConfigChanges = append(onConfigChanges, run)
}

// Invoke callbacks when config changed
func onConfigChanged(in fsnotify.Event) {
	for _, fn := range onConfigChanges {
		fn(in)
	}
}

func loadConfig() error {
	wLock.Lock()
	defer wLock.Unlock()

	logger := logger.Tag("Config")

	// Set up Viper to prioritize environment variables
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Support for keys provided in  the environment
	// A key in database1: password would have to be called DATABASE1_PASSWORD
	// The yaml value has to be empty for instance
	// database1:
	//    password:
	//
	// Real life example:
	// models:
	//   rds:
	//     databases:
	//       docker_db:
	//         type: postgresql
	//         database:
	//         username:
	//         password:
	// => Three env variables are to be instantiated:
	// MODELS_RDS_DATABASES_DOCKER_DB_DATABASE="database_name"
	// MODELS_RDS_DATABASES_DOCKER_DB_USERNAME="postgres
	// MODELS_RDS_DATABASES_DOCKER_DB_PASSWORD="password"
	// has to be set
	//
	// Rules:
	// - all UPPERCASE
	// - replace `:` with _
	// - make sure to include the complete hierarchy (start from /)

	err := viper.ReadInConfig()
	if err != nil {
		logger.Error("Load gobackup config failed: ", err)
		return err
	}

	viperConfigFile := viper.ConfigFileUsed()
	logger.Info("Config file:", viperConfigFile)
	if info, err := os.Stat(viperConfigFile); err == nil {
		// max permission: 0770
		if info.Mode()&(1<<2) != 0 {
			logger.Warnf("Other users are able to access %s with mode %v", viperConfigFile, info.Mode())
		}
	}

	// load .env if exists in the same directory of used config file and expand variables in the config
	dotEnv := filepath.Join(filepath.Dir(viperConfigFile), ".env")
	if _, err := os.Stat(dotEnv); err == nil {
		if err := godotenv.Load(dotEnv); err != nil {
			logger.Errorf("Load %s failed: %v", dotEnv, err)
			return err
		}
	}

	cfg, err := os.ReadFile(viperConfigFile)
	if err != nil {
		logger.Errorf("Error reading config file: %v", err)
		return err
	}

	if err := viper.ReadConfig(strings.NewReader(os.ExpandEnv(string(cfg)))); err != nil {
		logger.Errorf("Load expanded config failed: %v", err)
		return err
	}

	// TODO: Here the `useTempWorkDir` and `workdir`, is not in config document. We need removed it.
	viper.Set("useTempWorkDir", false)
	if workdir := viper.GetString("workdir"); len(workdir) == 0 {
		// use temp dir as workdir
		dir, err := os.MkdirTemp("", "gobackup")
		if err != nil {
			return err
		}

		viper.Set("workdir", dir)
		viper.Set("useTempWorkDir", true)
	}

	Exist = true
	Models = []ModelConfig{}
	for key := range viper.GetStringMap("models") {
		model, err := loadModel(key)
		if err != nil {
			return fmt.Errorf("load model %s: %v", key, err)
		}

		Models = append(Models, model)
	}

	if len(Models) == 0 {
		return fmt.Errorf("no model found in %s", viperConfigFile)
	}

	// Load web config
	Web = WebConfig{}
	viper.SetDefault("web.host", "0.0.0.0")
	viper.SetDefault("web.port", 2703)
	Web.Host = viper.GetString("web.host")
	Web.Port = viper.GetString("web.port")
	Web.Username = viper.GetString("web.username")
	Web.Password = viper.GetString("web.password")

	UpdatedAt = time.Now()
	logger.Infof("Config loaded, found %d models.", len(Models))

	return nil
}

func loadModel(key string) (ModelConfig, error) {
	var model ModelConfig
	model.Name = key

	workdir, _ := os.Getwd()

	model.WorkDir = workdir
	model.TempPath = filepath.Join(viper.GetString("workdir"), fmt.Sprintf("%d", time.Now().UnixNano()))
	model.DumpPath = filepath.Join(model.TempPath, key)
	model.Viper = viper.Sub("models." + key)

	model.Description = model.Viper.GetString("description")
	model.Schedule = ScheduleConfig{Enabled: false}

	model.CompressWith = SubConfig{
		Type:  model.Viper.GetString("compress_with.type"),
		Viper: model.Viper.Sub("compress_with"),
	}

	model.EncryptWith = SubConfig{
		Type:  model.Viper.GetString("encrypt_with.type"),
		Viper: model.Viper.Sub("encrypt_with"),
	}

	model.Archive = model.Viper.Sub("archive")
	model.Splitter = model.Viper.Sub("split_with")

	model.BeforeScript = model.Viper.GetString("before_script")
	model.AfterScript = model.Viper.GetString("after_script")

	loadScheduleConfig(&model)
	loadDatabasesConfig(&model)
	loadStoragesConfig(&model)

	if len(model.Storages) == 0 {
		return ModelConfig{}, fmt.Errorf("no storage found in model %s", model.Name)
	}

	loadNotifiersConfig(&model)

	return model, nil
}

func loadScheduleConfig(model *ModelConfig) {
	subViper := model.Viper.Sub("schedule")
	model.Schedule = ScheduleConfig{Enabled: false}
	if subViper == nil {
		return
	}

	model.Schedule = ScheduleConfig{
		Enabled: true,
		Cron:    subViper.GetString("cron"),
		Every:   subViper.GetString("every"),
		At:      subViper.GetString("at"),
	}
}

func loadDatabasesConfig(model *ModelConfig) {
	model.Databases = make(map[string]SubConfig)

	// Get the databases map from the model's Viper instance
	for dbKey := range model.Viper.GetStringMap("databases") {
		dbPath := fmt.Sprintf("models.%s.databases.%s", model.Name, dbKey)

		// Log the DB info content for debugging
		logger.Debug("Database config for", dbKey, ":", viper.GetString(fmt.Sprintf("%s.database", dbPath)))

		// Populate the database config
		model.Databases[dbKey] = SubConfig{
			Name:  dbKey,
			Type:  viper.GetString(fmt.Sprintf("%s.type", dbPath)),     // Use full path for 'type'
			Viper: model.Viper.Sub(fmt.Sprintf("databases.%s", dbKey)), // Keep the sub-viper for consistency
		}

		// Directly set the database fields using the full path
		// => We need to to this because Viper.Sub does not inherit the environment variable properly
		// It creates a new instance with no env variable available to overwrtite the otherwise empty values
		model.Databases[dbKey].Viper.Set("database", viper.GetString(fmt.Sprintf("%s.database", dbPath)))
		model.Databases[dbKey].Viper.Set("username", viper.GetString(fmt.Sprintf("%s.username", dbPath)))
		model.Databases[dbKey].Viper.Set("password", viper.GetString(fmt.Sprintf("%s.password", dbPath)))

		// Log the final database configuration for debugging
		logger.Debug("Database:", model.Databases[dbKey].Viper.GetString("database"))
		logger.Debug("Username:", model.Databases[dbKey].Viper.GetString("username"))

		logger.Info("Database config for", dbKey, ":", viper.GetString(fmt.Sprintf("%s.database", dbPath)))

	}

	if len(model.Databases) == 0 {
		logger.Error("No databases found for model:", model.Name)
	}
	// subViper := model.Viper.Sub("databases")
	// model.Databases = map[string]SubConfig{}
	// for key := range model.Viper.GetStringMap("databases") {
	// 	logger.Info("SubViper for database:", key)
	// 	logger.Info("Content:", model.Viper.AllSettings())

	// 	dbViper := subViper.Sub(key)
	// 	model.Databases[key] = SubConfig{
	// 		Name:  key,
	// 		Type:  dbViper.GetString("type"),
	// 		Viper: dbViper,
	// 	}
	// }
}

func loadStoragesConfig(model *ModelConfig) {
	storageConfigs := map[string]SubConfig{}

	model.DefaultStorage = model.Viper.GetString("default_storage")

	subViper := model.Viper.Sub("storages")
	for key := range model.Viper.GetStringMap("storages") {
		storageViper := subViper.Sub(key)
		storageConfigs[key] = SubConfig{
			Name:  key,
			Type:  storageViper.GetString("type"),
			Viper: storageViper,
		}

		// Set default storage
		if len(model.DefaultStorage) == 0 {
			model.DefaultStorage = key
		}
	}
	model.Storages = storageConfigs

}

func loadNotifiersConfig(model *ModelConfig) {
	subViper := model.Viper.Sub("notifiers")
	model.Notifiers = map[string]SubConfig{}
	for key := range model.Viper.GetStringMap("notifiers") {
		dbViper := subViper.Sub(key)
		model.Notifiers[key] = SubConfig{
			Name:  key,
			Type:  dbViper.GetString("type"),
			Viper: dbViper,
		}
	}
}

// GetModelConfigByName get model config by name
func GetModelConfigByName(name string) (model *ModelConfig) {
	for _, m := range Models {
		if m.Name == name {
			model = &m
			return
		}
	}
	return
}

// GetDatabaseByName get database config by name
func (model *ModelConfig) GetDatabaseByName(name string) (subConfig *SubConfig) {
	for _, m := range model.Databases {
		if m.Name == name {
			subConfig = &m
			return
		}
	}
	return
}
