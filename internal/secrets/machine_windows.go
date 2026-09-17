package secrets

import "golang.org/x/sys/windows/registry"

func nativeMachineID() (string, error) {
	k, e := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if e != nil {
		return "", e
	}
	defer k.Close()
	id, _, e := k.GetStringValue("MachineGuid")
	return id, e
}
