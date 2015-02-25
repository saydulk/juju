// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package systemd

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"strings"

	"github.com/coreos/go-systemd/dbus"
	"github.com/juju/errors"

	"github.com/juju/juju/juju/paths"
	"github.com/juju/juju/service/common"
	"github.com/juju/juju/version"
)

// ListServices returns the list of installed service names.
func ListServices() ([]string, error) {
	conn, err := newConn()
	if err != nil {
		return nil, errors.Trace(err)
	}
	defer conn.Close()

	units, err := conn.ListUnits()
	if err != nil {
		return nil, errors.Trace(err)
	}

	var services []string
	for _, unit := range units {
		// TODO(ericsnow) Will the unit names really always end with .service?
		if !strings.HasSuffix(unit.Name, ".service") {
			continue
		}
		name := strings.TrimSuffix(unit.Name, ".service")
		services = append(services, name)
	}
	return services, nil
}

// ListCommand returns a command that will list the services on a host.
func ListCommand() string {
	return `systemctl --no-legend --no-page -t service -a` +
		` | grep -o -P '^\w[-\w]*(?=\.service)'`
}

// Service provides visibility into and control over a systemd service.
type Service struct {
	common.Service

	ConfName string
	UnitName string
	Dirname  string
	Script   []byte
}

// NewService returns a new value that implements Service for systemd.
func NewService(name string, conf common.Conf) (*Service, error) {
	confName := name + ".service"
	dataDir, err := findDataDir()
	if err != nil {
		return nil, errors.Trace(err)
	}
	dirname := path.Join(dataDir, "init", name)

	service := &Service{
		Service: common.Service{
			Name: name,
			// Conf is set in setConf.
		},
		ConfName: confName,
		UnitName: confName,
		Dirname:  dirname,
	}

	if err := service.setConf(conf); err != nil {
		return service, errors.Trace(err)
	}

	return service, nil
}

var findDataDir = func() (string, error) {
	return paths.DataDir(version.Current.Series)
}

// dbusAPI exposes all the systemd API methods needed by juju.
type dbusAPI interface {
	Close()
	ListUnits() ([]dbus.UnitStatus, error)
	StartUnit(string, string, chan<- string) (int, error)
	StopUnit(string, string, chan<- string) (int, error)
	EnableUnitFiles([]string, bool, bool) (bool, []dbus.EnableUnitFileChange, error)
	DisableUnitFiles([]string, bool) ([]dbus.DisableUnitFileChange, error)
	GetUnitProperties(string) (map[string]interface{}, error)
	GetUnitTypeProperties(string, string) (map[string]interface{}, error)
}

var newConn = func() (dbusAPI, error) {
	return dbus.New()
}

var newChan = func() chan string {
	return make(chan string)
}

// Name implements service.Service.
func (s Service) Name() string {
	return s.Service.Name
}

// Conf implements service.Service.
func (s Service) Conf() common.Conf {
	return s.Service.Conf
}

// UpdateConfig implements Service.
func (s *Service) UpdateConfig(conf common.Conf) {
	s.setConf(conf) // We ignore any error (i.e. when validation fails).
}

func (s *Service) setConf(conf common.Conf) error {
	scriptPath := path.Join(s.Dirname, "exec-start.sh")

	normalConf, data := normalize(conf, scriptPath)
	if err := validate(s.Service.Name, normalConf); err != nil {
		return errors.Trace(err)
	}

	s.Service.Conf = normalConf
	s.Script = data
	return nil
}

// Installed implements Service.
func (s *Service) Installed() bool {
	names, err := ListServices()
	if err != nil {
		return false
	}
	for _, name := range names {
		if name == s.Service.Name {
			return true
		}
	}
	return false
}

// Exists implements Service.
func (s *Service) Exists() bool {
	same, err := s.check()
	if err != nil {
		return false
	}
	return same
}

func (s *Service) check() (bool, error) {
	conf, err := s.readConf()
	if err != nil {
		return false, errors.Trace(err)
	}
	return reflect.DeepEqual(s.Service.Conf, conf), nil
}

func (s *Service) readConf() (common.Conf, error) {
	var conf common.Conf

	conn, err := newConn()
	if err != nil {
		return conf, errors.Trace(err)
	}
	defer conn.Close()

	// go-systemd does not appear to provide an easy way to get
	// a list of UnitOption for an existing unit. So we have to
	// do build the list manually.

	opts, err := getUnitOptions(conn, s.UnitName, "Service")
	if err != nil {
		return conf, errors.Trace(err)
	}
	conf, err = deserializeOptions(opts)
	return conf, errors.Trace(err)
}

// Running implements Service.
func (s *Service) Running() bool {
	conn, err := newConn()
	if err != nil {
		return false
	}
	defer conn.Close()

	units, err := conn.ListUnits()
	if err != nil {
		return false
	}

	for _, unit := range units {
		if unit.Name == s.UnitName {
			return unit.LoadState == "loaded" && unit.ActiveState == "active"
		}
	}
	return false
}

// Start implements Service.
func (s *Service) Start() error {
	if !s.Installed() {
		return errors.NotFoundf("service " + s.Service.Name)
	}
	if s.Running() {
		return nil
	}

	conn, err := newConn()
	if err != nil {
		return errors.Trace(err)
	}
	defer conn.Close()

	statusCh := newChan()
	_, err = conn.StartUnit(s.UnitName, "fail", statusCh)
	if err != nil {
		return errors.Trace(err)
	}

	status := <-statusCh
	if status != "done" {
		return errors.Errorf("failed to start service %s", s.Service.Name)
	}

	return nil
}

// Stop implements Service.
func (s *Service) Stop() error {
	if !s.Running() {
		return nil
	}

	conn, err := newConn()
	if err != nil {
		return errors.Trace(err)
	}
	defer conn.Close()

	statusCh := newChan()
	_, err = conn.StopUnit(s.UnitName, "fail", statusCh)
	if err != nil {
		return errors.Trace(err)
	}

	status := <-statusCh
	if status != "done" {
		return errors.Errorf("failed to stop service %s", s.Service.Name)
	}

	return err
}

// StopAndRemove implements Service.
func (s *Service) StopAndRemove() error {
	if err := s.Stop(); err != nil {
		return errors.Trace(err)
	}
	err := s.Remove()
	return errors.Trace(err)
}

// Remove implements Service.
func (s *Service) Remove() error {
	if !s.Installed() {
		return nil
	}

	conn, err := newConn()
	if err != nil {
		return errors.Trace(err)
	}
	defer conn.Close()

	// TODO(ericsnow) We may need the original file name (or make sure
	// the unit conf is on the systemd search path.
	_, err = conn.DisableUnitFiles([]string{s.UnitName}, false)
	if err != nil {
		return errors.Trace(err)
	}

	if err := removeAll(s.Dirname); err != nil {
		return errors.Trace(err)
	}

	return nil
}

var removeAll = func(name string) error {
	return os.RemoveAll(name)
}

// Install implements Service.
func (s *Service) Install() error {
	if s.Installed() {
		same, err := s.check()
		if err != nil {
			return errors.Trace(err)
		}
		if same {
			return nil
		}
		// An old copy is already running so stop it first.
		if err := s.StopAndRemove(); err != nil {
			return errors.Annotate(err, "systemd: could not remove old service")
		}
	}

	filename, err := s.writeConf()
	if err != nil {
		return errors.Trace(err)
	}

	conn, err := newConn()
	if err != nil {
		return errors.Trace(err)
	}
	defer conn.Close()

	// TODO(ericsnow) We may need to use conn.LinkUnitFiles either
	// instead of or in conjunction with EnableUnitFiles.
	_, _, err = conn.EnableUnitFiles([]string{filename}, false, true)
	return errors.Trace(err)
}

func (s *Service) writeConf() (string, error) {
	data, err := serialize(s.UnitName, s.Service.Conf)
	if err != nil {
		return "", errors.Trace(err)
	}

	if err := mkdirAll(s.Dirname); err != nil {
		return "", errors.Trace(err)
	}
	filename := path.Join(s.Dirname, s.ConfName)

	if s.Script != nil {
		scriptPath := s.Service.Conf.ExecStart
		if err := createFile(scriptPath, s.Script, 0755); err != nil {
			return filename, errors.Trace(err)
		}
	}

	if err := createFile(filename, data, 0644); err != nil {
		return filename, errors.Trace(err)
	}

	return filename, nil
}

var mkdirAll = func(dirname string) error {
	return os.MkdirAll(dirname, 0755)
}

var createFile = func(filename string, data []byte, perm os.FileMode) error {
	return ioutil.WriteFile(filename, data, perm)
}

// InstallCommands implements Service.
func (s *Service) InstallCommands() ([]string, error) {
	//remote := NewService(s.Service.Name, s.Service.Conf)
	//remote.Dirname = ioutil.TempDir("", "juju-systemd-remote-")

	data, err := serialize(s.UnitName, s.Service.Conf)
	if err != nil {
		return nil, errors.Trace(err)
	}

	commands := []string{
		fmt.Sprintf("cat >> /tmp/%s << 'EOF'\n%sEOF\n", s.ConfName, data),
		// TODO(ericsnow) "Link" the unit file first?
		//  "systemd link /tmp/" + s.ConfName,
		"systemd start /tmp/" + s.ConfName,
	}
	return commands, nil
}
