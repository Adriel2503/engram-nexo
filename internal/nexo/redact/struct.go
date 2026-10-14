package redact

import (
	"encoding/json"
	"reflect"
	"time"
)

var (
	timeType = reflect.TypeOf(time.Time{})
	rawType  = reflect.TypeOf(json.RawMessage(nil))
)

// Struct limpia en el lugar TODOS los textos de un valor devuelto o exportado:
// cada campo string, cada elemento de listas, claves y valores de mapas, los
// campos de tipo any y los documentos json.RawMessage (claves y valores
// anidados). v debe ser un puntero (a una estructura, a una lista de
// estructuras, a un mapa...). Es la barrera de lectura de los registros
// históricos: no depende de que cada campo se haya enumerado a mano, así un
// campo nuevo queda cubierto sin tocar nada.
func Struct(v any) {
	if v == nil {
		return
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	walk(rv.Elem(), 0)
}

func walk(v reflect.Value, depth int) {
	if depth > 24 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(Text(v.String()))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			walk(v.Elem(), depth+1)
		}
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return
		}
		v.Set(reflect.ValueOf(redactValue(v.Elem().Interface())))
	case reflect.Struct:
		if v.Type() == timeType {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				walk(v.Field(i), depth+1)
			}
		}
	case reflect.Slice:
		if v.IsNil() {
			return
		}
		if v.Type() == rawType {
			if v.CanSet() {
				if clean := JSON(v.Bytes()); clean != nil {
					v.SetBytes(clean)
				} else {
					v.SetBytes(nil)
				}
			}
			return
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := 0; i < v.Len(); i++ {
			walk(v.Index(i), depth+1)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walk(v.Index(i), depth+1)
		}
	case reflect.Map:
		if v.IsNil() || !v.CanSet() {
			return
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			k := reflect.New(v.Type().Key()).Elem()
			k.Set(iter.Key())
			walk(k, depth+1)
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(iter.Value())
			walk(e, depth+1)
			out.SetMapIndex(k, e)
		}
		v.Set(out)
	}
}
