package storage

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var volumeID = regexp.MustCompile(`^pvc-[a-z0-9]([a-z0-9-]{0,57}[a-z0-9])?$`)

func renderExports(volumes map[string]int, legacy bool) (string, error) {
	var b strings.Builder
	b.WriteString("NFS_CORE_PARAM { Protocols = 4; NFS_Port = 2049; }\nNFSv4 { Grace_Period = 0; }\nLOG { Default_Log_Level = WARN; }\n")
	if legacy {
		b.WriteString(exportBlock(100, "/export", "/data"))
	}
	ids := make([]string, 0, len(volumes))
	for id := range volumes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := volumes[id]
		if !volumeID.MatchString(id) || n < 101 || n > 65535 {
			return "", fmt.Errorf("invalid volume export")
		}
		b.WriteString(exportBlock(n, "/mnt/cloud-volumes/"+id, "/data/volumes/"+id))
	}
	return b.String(), nil
}

func exportBlock(n int, path, pseudo string) string {
	return fmt.Sprintf("EXPORT { Export_Id = %d; Path = %s; Pseudo = %s; Access_Type = RW; Squash = No_Root_Squash; Protocols = 4; Transports = TCP; SecType = sys; FSAL { Name = VFS; } }\n", n, path, pseudo)
}
