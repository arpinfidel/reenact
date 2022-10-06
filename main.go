package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

type Function struct {
	Name string
	Args []Field
	Rets []Field
}

type Field struct {
	Name string
	Type string
}

type Interface struct {
	Name      string
	Functions []Function
}

const standard = `
type TraceController struct {
	// call id from ctx, caller name, interface name
	t  map[string]map[string]map[string][]string
	tl *sync.RWMutex
	fl *sync.Mutex
}

func NewTraceController() *TraceController {
	return &TraceController{
		t:  map[string]map[string]map[string][]string{},
		tl: &sync.RWMutex{},
		fl: &sync.Mutex{},
	}
}

func (t *TraceController) PrintTrace() {
	trace := ""
	for callID := range t.t {
		if callID == "" {
			callID = "<no uuid>"
		}
		trace += t.traceUUID(callID)
	}
	trace += "===========================================\n"
	// fmt.Println(trace)

	f, err := os.OpenFile("./trace.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println(err)
	}
	defer f.Close()

	_, err = f.WriteString(trace)
	if err != nil {
		fmt.Println(err)
	}
}

func (t *TraceController) traceUUID(uuid string) string {
	trace := ""
	trace += fmt.Sprintf("%s {\n", uuid)
	m := t.t[uuid]
	for fName, itfMap := range m {
		fNameSplit := strings.Split(fName, ".")
		fName = fNameSplit[len(fNameSplit)-1]
		trace += fmt.Sprintf("\t%s {\n", fName)
		for _, calls := range itfMap {
			for _, call := range calls {
				trace += fmt.Sprintf("\t\t%s\n", call)
			}
		}
		trace += fmt.Sprintln("\t}")
	}
	trace += fmt.Sprintln("}")

	return trace
}

func (t *TraceController) TraceUUID(uuid string) {
	f, err := os.OpenFile("./trace.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println(err)
	}
	defer f.Close()

	trace := t.traceUUID(uuid)
	trace += "===========================================\n"

	_, err = f.WriteString(trace)
	if err != nil {
		fmt.Println(err)
	}
}

type Trace struct {
	c      *TraceController
	useAny bool
}

type ctxKey int

const TraceIDKey ctxKey = 1
const TraceFuncKey ctxKey = 2
const TraceCallerKey ctxKey = 3

type TContext struct {
	context.Context
}

type PrintTraceFunc func()

func WithTraceContext(ctx context.Context) (PrintTraceFunc, TContext) {
	uuid := uuid.NewV4().String()
	ctx = context.WithValue(ctx, TraceIDKey, uuid)

	f := func() {}
	ctx = context.WithValue(ctx, TraceFuncKey, &f)

	cPtr, _, _, _ := runtime.Caller(1)
	cDet := runtime.FuncForPC(cPtr)
	cName := cDet.Name()
	ctx = context.WithValue(ctx, TraceCallerKey, cName)

	return func() {
			(*ctx.Value(TraceFuncKey).(*func()))()
		}, TContext{
			Context: ctx,
		}
}

func (t *Trace) trace(iName, fName string, indent int, args []interface{}, rets []interface{}) {
	argsStr := ""
	for i, f := range args {
		if i != 0 {
			argsStr += ", "
		}
		if t.useAny {
			argsStr += "gomock.Any()"
			continue
		}
		argsStr += strings.Join(strings.Split(fmt.Sprintf("%# v", pretty.Formatter(f)), "\n"), "\n"+strings.Repeat("\t", indent))
	}

	retsStr := ""
	for i, f := range rets {
		if i != 0 {
			retsStr += ", "
		}
		retsStr += strings.Join(strings.Split(fmt.Sprintf("%# v", pretty.Formatter(f)), "\n"), "\n"+strings.Repeat("\t", indent))
	}

	trace := fmt.Sprintf("mock%s.EXPECT().%s(%s).Return(%s)", iName, fName, argsStr, retsStr)

	cPtr, _, _, _ := runtime.Caller(2)
	cDet := runtime.FuncForPC(cPtr)
	cName := cDet.Name()
	callID := ""
	if len(args) > 0 {
		if ctx, ok := args[0].(context.Context); !ok {
			fmt.Printf("WARN: TRACE: %s doesn't have context\n", fName)
		} else {
			uuid := ctx.Value(TraceIDKey)
			if uuid == nil {
				fmt.Printf("WARN: TRACE: %s has context but trace not set\n", fName)
			} else {
				callID = uuid.(string)

				fp := ctx.Value(TraceFuncKey).(*func())
				f := func() {
					t.c.TraceUUID(callID)
				}
				*fp = f

				cName = ctx.Value(TraceCallerKey).(string)
			}
		}
	}

	t.c.tl.Lock()
	defer t.c.tl.Unlock()
	
	if t.c.t[callID] == nil {
		t.c.t[callID] = map[string]map[string][]string{}
	}
	if t.c.t[callID][cName] == nil {
		t.c.t[callID][cName] = map[string][]string{}
	}
	t.c.t[callID][cName][iName] = append(t.c.t[callID][cName][iName], trace)
	// t.c.PrintTrace()
}
`

const iTemplate = `
type Trace{{iName}} struct {
	i {{iName}}
	n string
	Trace
}

func NewTrace{{iName}}(t {{iName}}, useAny bool, c *TraceController) *Trace{{iName}} {
	return &Trace{{iName}}{
		i: t,
		n: "{{iName}}",
		Trace: Trace{
			c:      c,
			useAny: useAny,
		},
	}
}
`

const fTemplate = `
func (t *Trace%s) {{fName}}(%s) %s {
	{{newRets}} := t.i.{{fName}}({{newArgs}})
	t.trace(t.n, "{{fName}}", 2, []interface{}{{{newArgs}}}, []interface{}{{{newRets}}})
	return {{newRets}}
}
`

const fTemplateNoRet = `
func (t *Trace%s) {{fName}}(%s) %s {
	t.i.{{fName}}({{newArgs}})
	t.trace(t.n, "{{fName}}", 2, []interface{}{{{newArgs}}}, []interface{}{})
}
`

func getInterfaces(file string) []Interface {
	r := regexp.MustCompile(`(?m)^type (.*?) interface {\n([\S\s]*?)^}`)
	matches := r.FindAllStringSubmatch(string(file), -1)

	interfaces := []Interface{}
	for _, m := range matches {
		iName := m[1]
		iFuncs := []Function{}
		for _, f := range strings.Split(m[2], "\n") {
			iFuncStr := strings.TrimSpace(f)
			if iFuncStr == "" {
				continue
			}
			r := regexp.MustCompile(`(.*?)\((.*?)\)(.*)`)
			matches := r.FindStringSubmatch(iFuncStr)

			fName := matches[1]
			fArgsStr := matches[2]
			fRetsStr := matches[3]

			// println(fName)
			// println(fArgsStr)
			// println(fRetsStr)

			fArgs := []Field{}
			for _, f := range strings.Split(fArgsStr, ",") {
				f = strings.TrimSpace(f)
				ff := strings.Split(f, " ")
				if len(ff) == 2 {
					fArgs = append(fArgs, Field{
						Name: ff[0],
						Type: ff[1],
					})
					continue
				}
				fArgs = append(fArgs, Field{
					Name: ff[0],
				})
			}

			for i := len(fArgs) - 1; i > 0; i-- {
				if fArgs[i-1].Type == "" {
					fArgs[i-1].Type = fArgs[i].Type
				}
			}

			fRets := []Field{}
			for _, f := range strings.Split(strings.Trim(fRetsStr, "() "), ",") {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				ff := strings.Split(f, " ")
				if len(ff) == 2 {
					fRets = append(fRets, Field{
						Name: ff[0],
						Type: ff[1],
					})
					continue
				}
				fRets = append(fRets, Field{
					Type: ff[0],
				})
				// fmt.Printf("%#v\n", fRets)
			}

			iFuncs = append(iFuncs, Function{
				Name: fName,
				Args: fArgs,
				Rets: fRets,
			})
		}
		interfaces = append(interfaces, Interface{
			Name:      iName,
			Functions: iFuncs,
		})
	}

	return interfaces
}

func getPackageName(file string) string {
	r := regexp.MustCompile(`(?m)^package (.*)$`)
	matches := r.FindStringSubmatch(string(file))
	return matches[1]
}

func getImports(file string) []string {
	r := regexp.MustCompile(`(?m)^import \(([\s\S]*?)\)`)
	matches := r.FindAllStringSubmatch(string(file), -1)

	imports := []string{}
	for _, m := range matches {
		ims := strings.Split(m[1], "\n")
		for _, im := range ims {
			im = strings.TrimSpace(im)
			if im == "" {
				continue
			}
			imports = append(imports, im)
		}
	}

	r = regexp.MustCompile(`(?m)^import (.*)"(.*)"`)
	matches = r.FindAllStringSubmatch(string(file), -1)
	for _, m := range matches {
		imports = append(imports, strings.Split(m[0], "import ")[1])
	}

	return imports
}

func generateITrace(itf Interface) string {
	return strings.ReplaceAll(iTemplate, "{{iName}}", itf.Name)
}

func generateFTrace(iName string, f Function) string {
	newRets := ""
	rets := ""
	for i, r := range f.Rets {
		if i > 0 {
			newRets += ", "
			rets += ", "
		}
		newRets += fmt.Sprintf("r%d", i)
		rets += r.Type
	}
	if len(f.Rets) > 1 {
		rets = "(" + rets + ")"
	}

	newArgs := ""
	args := ""
	for i, a := range f.Args {
		if i > 0 {
			newArgs += ", "
			args += ", "
		}
		args += fmt.Sprintf("%s %s", a.Name, a.Type)
		newArgs += a.Name
	}

	t := fTemplate
	if len(rets) == 0 {
		t = fTemplateNoRet
	}
	format := strings.ReplaceAll(t, "{{fName}}", f.Name)
	format = strings.ReplaceAll(format, "{{newRets}}", newRets)
	format = strings.ReplaceAll(format, "{{newArgs}}", newArgs)

	return fmt.Sprintf(format, iName, args, rets)
}

func generateImports(imports []string) string {
	imMap := map[string]struct{}{}
	for _, im := range imports {
		imMap[im] = struct{}{}
	}
	defaultImports := []string{
		`"runtime"`,
		`"fmt"`,
		`uuid "github.com/satori/go.uuid"`,
		`"os"`,
		`"strings"`,
		`"sync"`,
		`"github.com/kr/pretty"`,
	}
	for _, im := range defaultImports {
		if _, ok := imMap[im]; !ok {
			imports = append(imports, im)
		}
	}
	return "import (\n\t" + strings.Join(imports, "\n\t") + "\n)\n"
}

func generate(file string) string {
	res := "package " + getPackageName(file) + "\n\n"
	imports := getImports(file)
	res += generateImports(imports)

	res += standard

	itfs := getInterfaces(string(file))
	for _, itf := range itfs {
		res += generateITrace(itf)
		for _, f := range itf.Functions {
			res += generateFTrace(itf.Name, f)
		}
	}

	return res
}

func main() {
	args := os.Args[1:]
	inPath := args[1]
	outPath := args[2]

	file, err := os.ReadFile(inPath)
	if err != nil {
		return
	}

	gen := generate(string(file))

	err = os.WriteFile(outPath, []byte(gen), 0644)
	if err != nil {
		return
	}
}
