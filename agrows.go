package main

import (
	"fmt"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"github.com/dave/jennifer/jen"
	"github.com/samber/lo"
	log "github.com/sett17/dnutlogger"
	flag "github.com/spf13/pflag"
)

type FuncInfo struct {
	OriginalIdentifier *dst.Ident
	Params             *dst.FieldList
	Results            *dst.FieldList
}

const prefixForModifiedFunction = "agrows_"

func (f FuncInfo) String() string {
	var sb strings.Builder

	sb.WriteString(f.OriginalIdentifier.Name)
	sb.WriteString("(")
	for i, param := range f.Params.List {
		if i > 0 {
			sb.WriteString(", ")
		}
		for j, name := range param.Names {
			if j > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(name.Name)
		}
		sb.WriteString(" ")
		sb.WriteString(param.Type.(*dst.Ident).Name)
	}
	sb.WriteString(") → (")
	for i, result := range f.Results.List {
		if i > 0 {
			sb.WriteString(", ")
		}
		for j, name := range result.Names {
			if j > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(name.Name)
		}
		if len(result.Names) > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(result.Type.(*dst.Ident).Name)
	}
	sb.WriteString(")")

	return sb.String()
}

func parseFileToTree(r io.Reader) (*dst.File, error) {
	fset := token.NewFileSet()
	file, err := decorator.ParseFile(fset, "", r, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func extractFuncInfo(node *dst.File) []FuncInfo {
	var funcs []FuncInfo
	dst.Inspect(node, func(n dst.Node) bool {
		if fn, ok := n.(*dst.FuncDecl); ok && fn.Name.IsExported() {
			originalIdentifier := *fn.Name
			var Params dst.FieldList
			if fn.Type.Params != nil {
				Params = *fn.Type.Params
			}
			var Results dst.FieldList
			if fn.Type.Results != nil {
				Results = *fn.Type.Results
			}
			funcInfo := FuncInfo{
				OriginalIdentifier: &originalIdentifier,
				Params:             &Params,
				Results:            &Results,
			}
			funcs = append(funcs, funcInfo)
		}
		return true
	})
	return funcs
}

func modifyOriginalFunctions(tree *dst.File) {
	dst.Inspect(tree, func(n dst.Node) bool {
		if fn, ok := n.(*dst.FuncDecl); ok && fn.Name.IsExported() {
			fn.Name.Name = prefixForModifiedFunction + fn.Name.Name
		}
		return true
	})
}

func removeOriginalAndUnexportedFunctions(tree *dst.File) {
	var decls []dst.Decl
	for _, decl := range tree.Decls {
		if _, ok := decl.(*dst.FuncDecl); !ok {
			decls = append(decls, decl)
		}
	}
	tree.Decls = decls
}

func generateNewClientFunc(info FuncInfo) *jen.Statement {
	modifiedFunctionName := prefixForModifiedFunction + info.OriginalIdentifier.Name
	_ = modifiedFunctionName
	fn := jen.Func().Id(info.OriginalIdentifier.Name).
		ParamsFunc(func(g *jen.Group) {
			for _, param := range info.Params.List {
				if len(param.Names) > 0 {
					g.Id(param.Names[0].Name).Qual("", param.Type.(*dst.Ident).Name)
				} else {
					g.Qual("", param.Type.(*dst.Ident).Name)
				}
			}
		}).
		ParamsFunc(func(g *jen.Group) {
			g.Error()
		}).
		Block(
			jen.Id("data").Op(",").Err().Op(":=").Qual("github.com/codeupdateandmodificationsystem/protocol", "EncodeFunctionCall").
				Call(
					jen.Lit(info.OriginalIdentifier.Name),
					jen.Qual("github.com/codeupdateandmodificationsystem/protocol", "Options").Call(),
					jen.Map(jen.String()).Any().ValuesFunc(func(g *jen.Group) {
						for _, param := range info.Params.List {
							g.Line().Lit(param.Names[0].Name).Op(":").Id(param.Names[0].Name)
						}
						g.Line()
					},
					),
				),
			jen.If(jen.Err().Op("!=").Nil()).Block(
				jen.Return(jen.Err()),
			),
			jen.Return(jen.Id("sendMessage").Call(jen.Id("data"))),
		)
	fn.Line()
	return fn
}

func generateJSSendMessageFunction() *jen.Statement {
	return jen.Func().Id("sendMessage").Params(jen.Id("data").Index().Byte()).Error().Block(
		jen.Id("jsGlobal").Op(":=").Qual("syscall/js", "Global").Call(),
		jen.Id("sendMessageFunc").Op(":=").Id("jsGlobal").Dot("Get").Call(jen.Lit("sendMessage")),
		jen.If(jen.Id("sendMessageFunc").Dot("Type").Call().Op("!=").Qual("syscall/js", "TypeFunction")).Block(
			jen.Return(jen.Qual("errors", "New").Call(jen.Lit("sendMessage is not a JS function"))),
		),
		jen.Id("uint8Array").Op(":=").Qual("syscall/js", "TypedArrayOf").Call(jen.Id("data")),
		jen.Defer().Id("uint8Array").Dot("Release").Call(),
		jen.Id("sendMessageFunc").Dot("Invoke").Call(jen.Id("uint8Array")),
	).Line()
}

func generateServerReceiver(infos []FuncInfo) *jen.Statement {
	return jen.Func().
		Id("AgrowsReceive").
		Params(jen.Id("data").Qual("", "[]byte")).
		Params(jen.String(), jen.Error()).
		Block(
			jen.List(jen.Id("functionName"), jen.Id("args"), jen.Id("err")).
				Op(":=").
				Qual("github.com/codeupdateandmodificationsystem/protocol", "DecodeFunctionCall").
				Call(jen.Id("data"), jen.Qual("github.com/codeupdateandmodificationsystem/protocol", "Options").Call()),

			jen.If(jen.Err().Op("!=").Nil()).Block(
				jen.Return(jen.Err()),
			),
			jen.Switch(jen.Id("functionName")).BlockFunc(func(generator *jen.Group) {
				for _, fnInfo := range infos {
					generator.Empty()
					generator.Case(jen.Lit(fnInfo.OriginalIdentifier.Name)).
						BlockFunc(func(caseGenerator *jen.Group) {
							for i, param := range fnInfo.Params.List {
								caseGenerator.Id("param" + fmt.Sprint(i)).Op(",").Id("ok").Op(":=").Id("args").Index(jen.Lit(i)).Assert(jen.Qual("", param.Type.(*dst.Ident).Name))
								caseGenerator.If(jen.Op("!").Id("ok")).Block(
									jen.Return(jen.Lit(""), jen.Qual("errors", "New").Call(
										jen.Qual("fmt", "Sprintf").Call(
											jen.Lit("failed to cast parameter '%s' to '%s'"),
											jen.Qual("github.com/codeupdateandmodificationsystem/protocol", "TypeToString").Index(jen.Id("args").Index(jen.Lit(i)).Dot("Typ")),
											jen.Lit(param.Type.(*dst.Ident).Name),
										),
									)),
								)
							}
							modifiedFunctionName := prefixForModifiedFunction + fnInfo.OriginalIdentifier.Name

							if len(fnInfo.Results.List) == 0 {
								caseGenerator.Id(modifiedFunctionName).CallFunc(func(callGenerator *jen.Group) {
									for i := range fnInfo.Params.List {
										callGenerator.Id("param" + fmt.Sprint(i))
									}
								})
								caseGenerator.Return(jen.Lit(""), jen.Nil())
								return
							}

							returnedErrorName := ""
							varNames := make([]string, len(fnInfo.Results.List))
							for i := range fnInfo.Results.List {
								if fnInfo.Results.List[i].Type.(*dst.Ident).Name == "error" {
									varNames[i] = "err" + fmt.Sprint(i)
									returnedErrorName = varNames[i]
									continue
								}
								varNames[i] = "ret" + fmt.Sprint(i)
							}

							caseGenerator.ListFunc(func(retGenerator *jen.Group) {
								for _, varName := range varNames {
									retGenerator.Id(varName)
								}
							}).Op(":=").Id(modifiedFunctionName).CallFunc(func(callGenerator *jen.Group) {
								for i := range fnInfo.Params.List {
									callGenerator.Id("param" + fmt.Sprint(i))
								}
							})

							if returnedErrorName != "" {
								caseGenerator.If(jen.Id(returnedErrorName).Op("!=").Nil()).Block(
									jen.Return(jen.Lit(""), jen.Id(returnedErrorName)),
								)
							}

							formattedReturns := jen.Qual("fmt", "Sprintf").Call(
								jen.Lit(fmt.Sprintf("'%%+v'%s", strings.Repeat(", '%+v'", len(varNames)-1))),
								jen.ListFunc(func(paramGenerator *jen.Group) {
									for _, varName := range varNames {
										paramGenerator.Id(varName)
									}
								}),
							)

							caseGenerator.Return(formattedReturns, jen.Nil())
						})
				}
				generator.Empty()
				generator.Default().Block(
					jen.Return(jen.Qual("errors", "New").Call(jen.Qual("fmt", "Sprintf").Call(jen.Lit("unknown function '%s'"), jen.Id("functionName")))),
				)
			}),
		)
}

func writeCombinedTreeAndGenerated(tree *dst.File, generated *jen.File, writer io.Writer, genType byte) (int, error) {
	var filePrefix string
	if genType == SERVER {
		filePrefix += `//go:build server
`
	} else {
		filePrefix += `//go:build js && wasm && client
`
	}

	filePrefix += fmt.Sprintf(`/*
	Code generated by agrows. DO NOT EDIT :)
	This code was generated on %s at %s
	Any changes made to this file will be lost
	*/
	`, time.Now().Format("2006-01-02"), time.Now().Format("15:04:05"))

	fset := token.NewFileSet()
	var builder strings.Builder
	err := generated.Render(&builder)
	if err != nil {
		return 0, fmt.Errorf("failed to render generated code: %v", err)
	}

	src := builder.String()
	parsedFile, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return 0, fmt.Errorf("failed to parse rendered code: %v", err)
	}

	genDst, err := decorator.DecorateFile(fset, parsedFile)
	if err != nil {
		return 0, fmt.Errorf("failed to convert parsed file to dst.File: %v", err)
	}

	sourceImportSpecs := make([]dst.Spec, 0)
	lo.ForEach(tree.Decls, func(item dst.Decl, _ int) {
		if genDecl, ok := item.(*dst.GenDecl); ok && genDecl.Tok == token.IMPORT {
			sourceImportSpecs = append(sourceImportSpecs, genDecl.Specs...)
		}
	})

	genImportSpecs := make([]dst.Spec, 0)
	lo.ForEach(genDst.Decls, func(item dst.Decl, _ int) {
		if genDecl, ok := item.(*dst.GenDecl); ok && genDecl.Tok == token.IMPORT {
			genImportSpecs = append(genImportSpecs, genDecl.Specs...)
		}
	})

	declsWithoutImports := lo.Filter(append(tree.Decls, genDst.Decls...), func(x dst.Decl, _ int) bool {
		if genDecl, ok := x.(*dst.GenDecl); ok {
			return genDecl.Tok != token.IMPORT
		}
		return true
	})

	var rebuildImportSpec dst.GenDecl
	if genType == SERVER {
		rebuildImportSpec = dst.GenDecl{
			Tok:    token.IMPORT,
			Specs:  append(sourceImportSpecs, genImportSpecs...),
			Lparen: true,
			Rparen: true,
		}
	} else {
		rebuildImportSpec = dst.GenDecl{
			Tok:    token.IMPORT,
			Specs:  genImportSpecs,
			Lparen: true,
			Rparen: true,
		}
	}

	rebuildCombinedDecls := append([]dst.Decl{&rebuildImportSpec}, declsWithoutImports...)

	combined := &dst.File{
		Name:  tree.Name,
		Decls: rebuildCombinedDecls,
	}

	fset, file, err := decorator.RestoreFile(combined)
	if err != nil {
		return 0, fmt.Errorf("failed to restore dst.File: %v", err)
	}

	var buffer strings.Builder

	buffer.WriteString(filePrefix)

	if err := printer.Fprint(&buffer, fset, file); err != nil {
		return 0, fmt.Errorf("failed to write to buffer: %v", err)
	}

	formattedFile, err := format.Source([]byte(buffer.String()))
	if err != nil {
		return 0, err
	}

	n, err := writer.Write(formattedFile)
	return n, err
}

const (
	SERVER byte = iota + 1
	CLIENT
)

func main() {
	inputParameter := flag.StringP("input", "i", "", "Input file (required)")
	outputParameter := flag.StringP("output", "o", "", "Output file (default: agrows_<server|client>_<input_file>)")
	debugParameter := flag.BoolP("dbg", "D", false, "Enable debug logging")

	serverCmd := flag.NewFlagSet("server", flag.ExitOnError)
	clientCmd := flag.NewFlagSet("client", flag.ExitOnError)

	flag.Parse()

	if *debugParameter {
		log.SetMinLevel(log.DEBUG)
		log.Debug("Debug logging enabled")
	}

	if *inputParameter == "" {
		printUsageAndExit("Error: --input parameter is required")
	}

	if flag.NArg() < 1 {
		printUsageAndExit("Error: expected 'server' or 'client' subcommand")
	}

	var generatorType byte
	switch flag.Arg(0) {
	case "server":
		err := serverCmd.Parse(flag.Args()[1:])
		if err != nil {
			log.Errorf(true, "Failed to parse 'server' subcommand: %v", err)
		}
		generatorType = SERVER
	case "client":
		err := clientCmd.Parse(flag.Args()[1:])
		if err != nil {
			log.Errorf(true, "Failed to parse 'client' subcommand: %v", err)
		}
		generatorType = CLIENT
	default:
		printUsageAndExit(fmt.Sprintf("Error: unknown subcommand '%s'", flag.Arg(0)))
	}

	var output io.Writer
	if *outputParameter == "" {
		var env string
		if generatorType == SERVER {
			env = "server"
		} else {
			env = "client"
		}
		fileName := filepath.Base(*inputParameter)
		filePath := filepath.Dir(*inputParameter)
		outputFile := filepath.Join(filePath, fmt.Sprintf("agrows_%s_%s", env, fileName))
		var err error
		output, err = os.Create(outputFile)
		if err != nil {
			log.Errorf(true, "Failed to create output file: %v", err)
		}
	} else if *outputParameter == "-" {
		output = os.Stdout
	} else {
		var err error
		output, err = os.Create(*outputParameter)
		if err != nil {
			log.Errorf(true, "Failed to create output file: %v", err)
		}
	}

	inputFile, err := os.Open(*inputParameter)
	if err != nil {
		log.Errorf(true, "Failed to open input file: %v", err)
	}
	defer inputFile.Close()

	tree, err := parseFileToTree(inputFile)
	if err != nil {
		log.Errorf(true, "Failed to parse file: %v", err)
	}

	funcInfos := extractFuncInfo(tree)

	newFile := jen.NewFile("main")
	switch generatorType {
	case SERVER:
		modifyOriginalFunctions(tree)
		newFile.Add(generateServerReceiver(funcInfos))
	case CLIENT:
		removeOriginalAndUnexportedFunctions(tree)
		for _, info := range funcInfos {
			newFile.Add(generateNewClientFunc(info))
		}
		newFile.Add(generateJSSendMessageFunction())
	}

	_, err = writeCombinedTreeAndGenerated(tree, newFile, output, generatorType)
	if err != nil {
		log.Errorf(true, "Failed to save combined file: %v", err)
	}
}

func printUsageAndExit(message string) {
	fmt.Fprintln(os.Stderr, message)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "agrows - Almost Good RPC Over WebSockets")
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  agrows --input <input_file> [--output <output_file>] [--dbg] <server|client>")
	fmt.Fprintln(os.Stderr, "Flags:")
	flag.PrintDefaults()
	os.Exit(1)
}
