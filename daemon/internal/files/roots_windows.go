package files

import "golang.org/x/sys/windows"

// roots returns the drives, such as C:\.
func roots() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return []string{`C:\`}
	}
	var drives []string
	for i := range 26 {
		if mask&(1<<i) != 0 {
			drives = append(drives, string(rune('A'+i))+`:\`)
		}
	}
	return drives
}
