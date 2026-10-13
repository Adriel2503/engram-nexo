package ingest

import "strings"

// fakeSecret arma en tiempo de ejecución las cadenas con forma de secreto que
// usan las pruebas de DetectSecrets. Así no quedan como literales contiguos
// en el binario de prueba: algunos antivirus (Kaspersky,
// "VHO:Trojan-PSW.Win32.Convagent.gen") ponían ingest.test.exe en cuarentena
// por esas cadenas. El compilador no pliega strings.Join sobre un slice.
func fakeSecret(parts ...string) string { return strings.Join(parts, "") }
