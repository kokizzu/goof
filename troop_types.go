package goof

import (
	"debug/dwarf"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unsafe"

	"github.com/zeebo/errs"
)

func (t *Troop) typelinks() ([]unsafe.Pointer, [][]uint32, error) {
	reader := t.data.Reader()
	for {
		entry, err := reader.Next()
		if err != nil {
			return nil, nil, errs.Wrap(err)
		} else if entry == nil {
			break
		} else if entry.Tag != dwarf.TagSubprogram {
			continue
		}
		name, ok := entry.Val(dwarf.AttrName).(string)
		if !ok || name != "reflect.typelinks" {
			continue
		}
		pc, ok := entry.Val(dwarf.AttrLowpc).(uint64)
		if !ok {
			continue
		}
		fn_typ := reflect.FuncOf(nil, []reflect.Type{
			reflect.TypeOf([]unsafe.Pointer(nil)),
			reflect.TypeOf([][]uint32(nil)),
		}, false)
		fn := reflect.ValueOf(makeInterface(dataPtr(fn_typ), unsafe.Pointer(&pc)))
		out := ifaces(fn.Call(nil))
		if len(out) != 2 {
			return nil, nil, errs.New("wrong number of output results: %d", len(out))
		}
		sections, ok := out[0].([]unsafe.Pointer)
		if !ok {
			return nil, nil, errs.New("wrong type of sections: %T", out[0])
		}
		offsets, ok := out[1].([][]uint32)
		if !ok {
			return nil, nil, errs.New("wrong type of offsets: %T", out[1])
		}
		return sections, offsets, nil
	}
	return nil, nil, errs.New("unable to find reflect.typelinks")
}

func (t *Troop) addTypes() error {
	sections, offset, err := t.typelinks()
	if err != nil {
		return err
	}
	for i, offs := range offset {
		section := sections[i]
		for _, off := range offs {
			ptr := unsafe.Pointer(uintptr(section) + uintptr(off))
			typ := reflect.TypeOf(makeInterface(ptr, nil))
			t.addType(typ)
		}
	}

	// special cased types
	t.types["*void"] = unsafePointerType
	t.types["**void"] = reflect.PtrTo(unsafePointerType)

	return nil
}

func (t *Troop) addType(typ reflect.Type) {
	name := dwarfName(typ)
	if _, ok := t.types[name]; ok {
		return
	}
	t.types[name] = typ

	defer func() {
		if r := recover(); r != nil {
			t.failures[name] = fmt.Errorf("failed to add type %q (%v): %v", name, typ, r)
		}
	}()

	switch typ.Kind() {
	case reflect.Ptr, reflect.Chan, reflect.Slice, reflect.Array:
		t.addType(typ.Elem())

	case reflect.Map:
		t.addType(typ.Key())
		t.addType(typ.Elem())

	case reflect.Func:
		for i := 0; i < typ.NumIn(); i++ {
			t.addType(typ.In(i))
		}
		for i := 0; i < typ.NumOut(); i++ {
			t.addType(typ.Out(i))
		}

	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			t.addType(typ.Field(i).Type)
		}
		for i := 0; i < typ.NumMethod(); i++ {
			t.addType(typ.Method(i).Type)
		}

	case reflect.Interface:
		for i := 0; i < typ.NumMethod(); i++ {
			t.addType(typ.Method(i).Type)
		}
	}
}

func (t *Troop) Types() ([]reflect.Type, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	out := make([]reflect.Type, 0, len(t.types))
	for _, typ := range t.types {
		out = append(out, typ)
	}
	sort.Sort(typesByString(out))
	return out, nil
}

func (t *Troop) Type(name string) (reflect.Type, error) {
	if err := t.check(); err != nil {
		return nil, err
	}

	entry, ok := t.types[name]
	if !ok {
		return nil, errs.New("type %s: unknown type", name)
	}

	return entry, nil
}

func (t *Troop) findDwarfTypes(dtypes []dwarf.Type) ([]reflect.Type, error) {
	out_types := make([]reflect.Type, 0, len(dtypes))
	for _, dtyp := range dtypes {
		typ, err := t.findDwarfType(dtyp)
		if err != nil {
			return nil, err
		}
		out_types = append(out_types, typ)
	}
	return out_types, nil
}

func (t *Troop) findDwarfType(dtyp dwarf.Type) (reflect.Type, error) {
	// TODO(jeff): we can synthesize some of these dwarf types at runtime,
	// but hopefully we got enough of them when we loaded up all of the
	// types. The problematic types are: 1. named types, 2. recursive types.
	var dname string
	switch dtyp := dtyp.(type) {
	case *dwarf.StructType:
		if dtyp.StructName != "" {
			dname = dtyp.StructName
		} else {
			dname = dtyp.Defn()
		}
	default:
		dname = dtyp.String()
	}

	// heh this is super hacky, but what isn't!?
	if strings.HasPrefix(dname, "*struct ") &&
		!strings.HasPrefix(dname, "*struct {") {

		dname = "*" + dname[len("*struct "):]
	}

	typ, ok := t.types[dname]
	if !ok {
		return nil, errs.New("dwarf type %q unknown", dname)
	}
	return typ, nil
}
