package osenv_test

import (
	gc "launchpad.net/gocheck"

	"launchpad.net/juju-core/juju/osenv"
	"launchpad.net/juju-core/testing"
)

func (*importSuite) TestHomeLinux(c *gc.C) {
	h := "/home/foo/bar"
	testing.PatchEnvironment("HOME", h)
	c.Check(osenv.Home(), gc.Equals, h)
}
