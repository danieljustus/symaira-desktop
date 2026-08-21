package version

import (
	"github.com/danieljustus/symaira-corekit/versionkit"
)

var Version = "dev"

const SchemaVersion = 1

func GetInfo() versionkit.Info {
	return versionkit.New("symroom", Version, SchemaVersion)
}
