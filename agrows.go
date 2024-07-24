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

type ParamReflectInfo struct {
	DstField *dst.Field
	IsStruct bool
}

func (p *ParamReflectInfo) String() string {
	if len(p.DstField.Names) > 0 && p.DstField.Names[0] != nil {
		return fmt.Sprintf("%s isStruct: %t", p.DstField.Names[0].Name, p.IsStruct)
	}

	return fmt.Sprintf("%s isStruct: %t", p.DstField.Type.(*dst.Ident).Name, p.IsStruct)
}

type FuncInfo struct {
	OriginalIdentifier *dst.Ident
	Params             []*ParamReflectInfo
	Results            []*ParamReflectInfo
}

func (f *FuncInfo) String() string {
	var params []string
	var results []string

	for _, param := range f.Params {
		params = append(params, param.String())
	}

	for _, result := range f.Results {
		results = append(results, result.String())
	}

	return fmt.Sprintf("func %s(%s) (%s)",
		f.ToIdentifierString(),
		strings.Join(params, ", "),
		strings.Join(results, ", "))
}

func (f *FuncInfo) ToIdentifierString() string {
	if f.OriginalIdentifier != nil {
		return f.OriginalIdentifier.Name
	}
	return ""
}

type Input struct {
	FileName  string
	Functions []FuncInfo
	TypeMap   map[string]dst.Node
}

const modifiedFunctionFormat = "agrows_%s"
const wrapperFunctionFormat = "%sWrapper"

func parseFileToTree(r io.Reader) (*dst.File, error) {
	fset := token.NewFileSet()
	file, err := decorator.ParseFile(fset, "", r, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func isStruct(typeMap map[string]dst.Node, expr dst.Expr) bool {
	switch t := expr.(type) {
	case *dst.StructType:
		return true
	case *dst.Ident:
		if node, exists := typeMap[t.Name]; exists {
			if _, ok := node.(*dst.StructType); ok {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func extractTypeMap(node *dst.File) map[string]dst.Node {
	typeMap := make(map[string]dst.Node)
	dst.Inspect(node, func(n dst.Node) bool {
		switch decl := n.(type) {
		case *dst.GenDecl:
			for _, spec := range decl.Specs {
				if ts, ok := spec.(*dst.TypeSpec); ok {
					typeMap[ts.Name.Name] = ts.Type
				}
			}
		}
		return true
	})
	return typeMap
}

func extractFuncInfo(node *dst.File, typeMap map[string]dst.Node) []FuncInfo {
	var funcs []FuncInfo

	dst.Inspect(node, func(n dst.Node) bool {
		if fn, ok := n.(*dst.FuncDecl); ok && fn.Name.IsExported() {
			originalIdentifier := *fn.Name

			funcInfo := FuncInfo{
				OriginalIdentifier: &originalIdentifier,
				Params:             []*ParamReflectInfo{},
				Results:            []*ParamReflectInfo{},
			}

			if fn.Type.Params != nil {
				for _, param := range fn.Type.Params.List {
					for _, name := range param.Names {
						funcInfo.Params = append(funcInfo.Params, &ParamReflectInfo{
							DstField: &dst.Field{
								Names: []*dst.Ident{name},
								Type:  param.Type,
							},
							IsStruct: isStruct(typeMap, param.Type),
						})
					}
				}
			}

			if fn.Type.Results != nil {
				for _, result := range fn.Type.Results.List {
					var resultName *dst.Ident
					if result.Names != nil {
						resultName = result.Names[0]
					}
					funcInfo.Results = append(funcInfo.Results, &ParamReflectInfo{
						DstField: &dst.Field{
							Names: []*dst.Ident{resultName},
							Type:  result.Type,
						},
						IsStruct: isStruct(typeMap, result.Type),
					})
				}
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
			fn.Name.Name = fmt.Sprintf(modifiedFunctionFormat, fn.Name.Name)
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
	fn := jen.Func().Id(info.OriginalIdentifier.Name).
		ParamsFunc(func(g *jen.Group) {
			for _, paramInfo := range info.Params {
				param := paramInfo.DstField
				if len(param.Names) > 0 {
					g.Id(param.Names[0].Name).Qual("", param.Type.(*dst.Ident).Name)
				} else {
					g.Qual("", param.Type.(*dst.Ident).Name)
				}
			}
		}).
		ParamsFunc(func(g *jen.Group) {
			g.Any()
		}).
		Block(
			jen.Id("data").Op(",").Err().Op(":=").Qual("github.com/codeupdateandmodificationsystem/protocol", "EncodeFunctionCall").
				Call(
					jen.Lit(info.OriginalIdentifier.Name),
					jen.Qual("github.com/codeupdateandmodificationsystem/protocol", "Options").Call(),
					jen.Map(jen.String()).Any().ValuesFunc(func(g *jen.Group) {
						for _, paramInfo := range info.Params {
							param := paramInfo.DstField
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

	exposedFn := jen.Func().Id(fmt.Sprintf(wrapperFunctionFormat, info.OriginalIdentifier.Name)).
		Params(
			jen.Id("this").Qual("syscall/js", "Value"),
			jen.Id("p").Index().Qual("syscall/js", "Value"),
		).Params(jen.Any()).
		BlockFunc(func(g *jen.Group) {

			paramCount := len(info.Params)
			g.If(jen.Len(jen.Id("p")).Op("!=").Lit(paramCount)).Block(
				jen.Return(generateJsGlobalError(jen.Qual("fmt", "Sprintf").Call(jen.Lit(fmt.Sprintf("expected %d arguments, got %%d", paramCount)), jen.Len(jen.Id("p"))))),
			)
			for i, paramInfo := range info.Params {
				param := paramInfo.DstField
				if len(param.Names) > 0 {
					paramName := param.Names[0].Name
					paramNameAsAny := paramName + "AsAny"
					g.Id(paramNameAsAny).Op(",").Err().Op(":=").Id("jsValueToAny").Call(jen.Id("p").Index(jen.Lit(i)), jen.Qual("reflect", "TypeOf").Call(jen.Parens(jen.Op("*").Qual("", param.Type.(*dst.Ident).Name)).Call(jen.Nil())).Dot("Elem").Call())
					g.If(jen.Err().Op("!=").Nil()).Block(
						jen.Return(generateJsGlobalError(jen.Qual("fmt", "Sprintf").Call(jen.Lit(fmt.Sprintf("failed to make go type '%s' from js value: %%+v", param.Type.(*dst.Ident).Name)), jen.Err()))),
					)
					g.Id(paramName).Op(",").Id("ok").Op(":=").Id(paramNameAsAny).Assert(jen.Qual("", param.Type.(*dst.Ident).Name))
					g.If(jen.Op("!").Id("ok")).Block(
						jen.Return(generateJsGlobalError(jen.Qual("fmt", "Sprintf").Call(jen.Lit(fmt.Sprintf("parameter '%s' is not in the received arguments", paramName))))),
					)
				}
			}
			g.Return(
				jen.Id(info.OriginalIdentifier.Name).
					ParamsFunc(func(g *jen.Group) {
						for _, paramInfo := range info.Params {
							param := paramInfo.DstField
							if len(param.Names) > 0 {
								g.Id(param.Names[0].Name)
							}
						}
					}),
			)
		})
	exposedFn.Line()

	return jen.Add(fn, exposedFn)
}

func generateClientMain(funcInfos []FuncInfo) *jen.Statement {
	fn := jen.Func().Id("main").Params().BlockFunc(func(g *jen.Group) {
		g.Id("global").Op(":=").Qual("syscall/js", "Global").Call()
		for _, fnInfo := range funcInfos {
			g.Id("global").Dot("Set").Call(jen.Lit(fnInfo.OriginalIdentifier.Name), jen.Qual("syscall/js", "FuncOf").Call(jen.Id(fmt.Sprintf(wrapperFunctionFormat, fnInfo.OriginalIdentifier.Name))))
			g.Id("println").Call(jen.Lit(fmt.Sprintf("AGROWS: '%s(%s)' function registered", fnInfo.OriginalIdentifier.Name, lo.Reduce(fnInfo.Params, func(agg string, item *ParamReflectInfo, i int) string {
				agg += item.DstField.Type.(*dst.Ident).Name
				if i < len(fnInfo.Params)-1 {
					agg += ", "
				}
				return agg
			}, ""))))
		}
		g.Line()
		g.Select().Block()
	})

	return fn
}

func generateJSSendMessageFunction() *jen.Statement {
	return jen.Func().Id("sendMessage").Params(jen.Id("data").Index().Byte()).Any().Block(
		jen.Id("jsGlobal").Op(":=").Qual("syscall/js", "Global").Call(),
		jen.Id("sendMessageFunc").Op(":=").Id("jsGlobal").Dot("Get").Call(jen.Lit("sendMessage")),
		jen.If(jen.Id("sendMessageFunc").Dot("Type").Call().Op("!=").Qual("syscall/js", "TypeFunction")).Block(
			jen.Return(generateJsGlobalError(jen.Lit("sendMessage is not a JS function"))),
		),
		jen.Id("uint8Array").Op(":=").Qual("syscall/js", "Global").Call().Dot("Get").Call(jen.Lit("Uint8Array")).Dot("New").Call(jen.Len(jen.Id("data"))),
		jen.Qual("syscall/js", "CopyBytesToJS").Call(jen.Id("uint8Array"), jen.Id("data")),
		jen.Id("sendMessageFunc").Dot("Invoke").Call(jen.Id("uint8Array")),
		jen.Return(jen.Nil()),
	).Line()
}

func generateJsGlobalError(errMsg jen.Code) jen.Code {
	return jen.Qual("syscall/js", "Global").Call().Dot("Get").Call(jen.Lit("Error")).Dot("New").Call(errMsg)
}

func generateJsValueToAny() *jen.Statement {
	return jen.Func().Id("jsValueToAny").Params(
		jen.Id("v").Qual("syscall/js", "Value"),
		jen.Id("targetType").Qual("reflect", "Type"),
	).Params(
		jen.Any(), jen.Any(),
	).BlockFunc(func(g *jen.Group) {
		g.Switch(jen.Id("v").Dot("Type").Call()).BlockFunc(func(f *jen.Group) {
			f.Case(jen.Qual("syscall/js", "TypeBoolean")).BlockFunc(func(g *jen.Group) {
				g.Return(jen.Id("v").Dot("Bool").Call(), jen.Nil())
			})
			f.Case(jen.Qual("syscall/js", "TypeNumber")).BlockFunc(func(h *jen.Group) {
				h.Switch(jen.Id("targetType").Dot("Kind").Call()).Block(
					jen.Case(jen.Qual("reflect", "Int"), jen.Qual("reflect", "Int8"), jen.Qual("reflect", "Int16"), jen.Qual("reflect", "Int32"), jen.Qual("reflect", "Int64")).BlockFunc(func(i *jen.Group) {
						i.Return(jen.Id("int").Call(jen.Id("v").Dot("Int").Call()), jen.Nil())
					}),
					jen.Case(jen.Qual("reflect", "Uint"), jen.Qual("reflect", "Uint8"), jen.Qual("reflect", "Uint16"), jen.Qual("reflect", "Uint32"), jen.Qual("reflect", "Uint64")).BlockFunc(func(i *jen.Group) {
						i.Return(jen.Id("uint").Call(jen.Id("v").Dot("Int").Call()), jen.Nil())
					}),
					jen.Case(jen.Qual("reflect", "Float32"), jen.Qual("reflect", "Float64")).BlockFunc(func(i *jen.Group) {
						i.Return(jen.Id("v").Dot("Float").Call(), jen.Nil())
					}),
					jen.Default().BlockFunc(func(i *jen.Group) {
						i.Return(jen.Id("v").Dot("Float").Call(), jen.Nil())
					}),
				)
			})
			f.Case(jen.Qual("syscall/js", "TypeString")).BlockFunc(func(g *jen.Group) {
				g.Return(jen.Id("v").Dot("String").Call(), jen.Nil())
			})
			f.Case(jen.Qual("syscall/js", "TypeObject")).BlockFunc(func(h *jen.Group) {
				h.Id("result").Op(":=").Make(jen.Map(jen.String()).Any())
				h.Id("keys").Op(":=").Qual("syscall/js", "Global").Call().Dot("Get").Call(jen.Lit("Object")).Dot("Call").Call(jen.Lit("keys"), jen.Id("v"))
				h.For(jen.Id("i").Op(":=").Lit(0), jen.Id("i").Op("<").Id("keys").Dot("Length").Call(), jen.Id("i").Op("++")).BlockFunc(func(i *jen.Group) {
					i.Id("key").Op(":=").Id("keys").Dot("Index").Call(jen.Id("i")).Dot("String").Call()
					i.Id("value").Op(",").Err().Op(":=").Id("jsValueToAny").Call(jen.Id("v").Dot("Get").Call(jen.Id("key")), jen.Qual("reflect", "TypeOf").Call(jen.Parens(jen.Op("*").Any()).Call(jen.Nil())).Dot("Elem").Call())
					i.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Nil(), jen.Err()))
					i.Id("result").Index(jen.Id("key")).Op("=").Id("value")
				})
				h.List(jen.Id("jsonData"), jen.Err()).Op(":=").Qual("encoding/json", "Marshal").Call(jen.Id("result"))
				h.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Nil(), generateJsGlobalError(jen.Qual("fmt", "Sprintf").Call(jen.Lit("failed to marshal js object to json: %v"), jen.Err()))))
				h.Id("targetValue").Op(":=").Qual("reflect", "New").Call(jen.Id("targetType")).Dot("Interface").Call()
				h.Err().Op("=").Qual("encoding/json", "Unmarshal").Call(jen.Id("jsonData"), jen.Id("targetValue"))
				h.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Nil(), generateJsGlobalError(jen.Qual("fmt", "Sprintf").Call(jen.Lit("failed to unmarshal json to target type: %v"), jen.Err()))))
				h.Return(jen.Qual("reflect", "ValueOf").Call(jen.Id("targetValue")).Dot("Elem").Call().Dot("Interface").Call(), jen.Nil())
			})
			f.Case(jen.Qual("syscall/js", "TypeFunction")).BlockFunc(func(g *jen.Group) {
				g.Return(jen.Id("v"), jen.Nil())
			})
			f.Case(jen.Qual("syscall/js", "TypeUndefined"), jen.Qual("syscall/js", "TypeNull")).BlockFunc(func(g *jen.Group) {
				g.Return(jen.Nil(), jen.Nil())
			})
			f.Default().BlockFunc(func(g *jen.Group) {
				g.Return(jen.Nil(), generateJsGlobalError(jen.Qual("fmt", "Sprintf").Call(jen.Lit("unsupported js value type: %s"), jen.Id("v").Dot("Type").Call())))
			})
		})
	}).Line()
}

func generateServerReceiver(infos []FuncInfo) *jen.Statement {
	return jen.Func().
		Id("AgrowsReceive").
		Params(jen.Id("data").Qual("", "[]byte")).
		Params(jen.String(), jen.Error()).
		Block(
			jen.List(jen.Id("functionName"), jen.Id("args"), jen.Err()).
				Op(":=").
				Qual("github.com/codeupdateandmodificationsystem/protocol", "DecodeFunctionCall").
				Call(jen.Id("data"), jen.Qual("github.com/codeupdateandmodificationsystem/protocol", "Options").Call()),

			jen.If(jen.Err().Op("!=").Nil()).Block(
				jen.Return(jen.Lit(""), jen.Err()),
			),

			jen.Switch(jen.Id("functionName")).BlockFunc(func(generator *jen.Group) {
				for _, fnInfo := range infos {
					generator.Empty()
					generator.Case(jen.Lit(fnInfo.OriginalIdentifier.Name)).
						BlockFunc(func(caseGenerator *jen.Group) {
							if len(fnInfo.Params) != 0 {
								caseGenerator.Var().DefsFunc(func(g *jen.Group) {
									for _, paramInfo := range fnInfo.Params {
										param := paramInfo.DstField
										originalParamName := param.Names[0].Name
										paramName := originalParamName + "Param"
										paramType := param.Type.(*dst.Ident).Name
										paramNameArg := paramName + "Arg"

										g.Id(paramName).Qual("", paramType)
										g.Id(paramNameArg).Qual("github.com/codeupdateandmodificationsystem/protocol", "Argument")

										if paramInfo.IsStruct {
											paramNameValue := paramName + "Value"
											paramValue := originalParamName + "Value"
											g.Id(paramNameValue).Op(",").Id(paramValue).Qual("reflect", "Value")
										}
									}
									g.Id("ok").Bool()
								})
							}
							for _, paramInfo := range fnInfo.Params {
								param := paramInfo.DstField
								originalParamName := param.Names[0].Name
								paramName := originalParamName + "Param"
								paramType := param.Type.(*dst.Ident).Name
								paramNameArg := paramName + "Arg"
								paramNameValue := paramName + "Value"
								paramValue := originalParamName + "Value"

								if paramInfo.IsStruct {
									caseGenerator.Id(paramNameValue).Op("=").Qual("reflect", "ValueOf").Call(jen.Id(paramNameArg).Dot("Value"))

									_ = paramType
									caseGenerator.Id(paramValue).Op("=").Qual("reflect", "ValueOf").Call(jen.Op("&").Id(paramName)).Dot("Elem").Call()

									caseGenerator.For(jen.Id("i").Op(":=").Lit(0), jen.Id("i").Op("<").Id(paramValue).Dot("NumField").Call(), jen.Id("i").Op("++")).BlockFunc(func(g *jen.Group) {
										g.Id("key").Op(":=").Id(paramValue).Dot("Type").Call().Dot("Field").Call(jen.Id("i")).Dot("Name")
										g.Id("paramValueField").Op(":=").Id(paramNameValue).Dot("FieldByName").Call(jen.Id("key"))
										g.Id("fieldValue").Op(":=").Id(paramValue).Dot("Field").Call(jen.Id("i"))
										g.If(jen.Id("fieldValue").Dot("CanSet").Call()).Block(
											jen.Id(paramValue).Dot("Field").Call(jen.Id("i")).Dot("Set").Call(jen.Id("paramValueField")),
										)
									})
								} else {
									caseGenerator.If(jen.Id(paramNameArg).Op(",").Id("ok").Op("=").Id("args").Index(jen.Lit(originalParamName)).Op(";").Op("!").Id("ok").Block(
										jen.Return(jen.Lit(""), jen.Qual("errors", "New").Call(
											jen.Qual("fmt", "Sprintf").Call(
												jen.Lit("parameter %s is not in the received arguments"),
												jen.Lit(originalParamName),
											),
										)),
									))

									caseGenerator.If(jen.Id(paramName).Op(",").Id("ok").Op("=").Id(paramNameArg).Op(".").Qual("", "Value").Assert(jen.Qual("", paramType)).Op(";").Op("!").Id("ok").Block(
										jen.Return(jen.Lit(""), jen.Qual("errors", "New").Call(
											jen.Qual("fmt", "Sprintf").Call(
												jen.Lit("failed to cast parameter '%s' to '%s'"),
												jen.Id(paramName),
												jen.Lit(paramType),
											),
										)),
									))
								}

							}
							modifiedFunctionName := fmt.Sprintf(modifiedFunctionFormat, fnInfo.OriginalIdentifier.Name)

							if len(fnInfo.Results) == 0 {
								caseGenerator.Id(modifiedFunctionName).CallFunc(func(callGenerator *jen.Group) {
									for _, paramInfo := range fnInfo.Params {
										param := paramInfo.DstField
										callGenerator.Id(param.Names[0].Name + "Param")
									}
								})
								caseGenerator.Return(jen.Lit(""), jen.Nil())
								return
							}

							firstReturnedError := ""
							firstReturnedString := ""
							varNames := make([]string, len(fnInfo.Results))
							for i := range fnInfo.Results {
								if fnInfo.Results[i].DstField.Type.(*dst.Ident).Name == "error" {
									varNames[i] = "err" + fmt.Sprint(i)
									firstReturnedError = varNames[i]
									continue
								}
								if fnInfo.Results[i].DstField.Type.(*dst.Ident).Name == "string" {
									varNames[i] = "str" + fmt.Sprint(i)
									firstReturnedString = varNames[i]
									continue
								}
								varNames[i] = "ret" + fmt.Sprint(i)
							}

							caseGenerator.ListFunc(func(retGenerator *jen.Group) {
								for _, varName := range varNames {
									retGenerator.Id(varName)
								}
							}).Op(":=").Id(modifiedFunctionName).CallFunc(func(callGenerator *jen.Group) {
								for _, paramInfo := range fnInfo.Params {
									param := paramInfo.DstField
									callGenerator.Id(param.Names[0].Name + "Param")
								}
							})

							if firstReturnedError != "" {
								caseGenerator.If(jen.Id(firstReturnedError).Op("!=").Nil()).Block(
									jen.Return(jen.Lit(""), jen.Id(firstReturnedError)),
								)
							}

							var strReturn *jen.Statement

							if firstReturnedString != "" {
								strReturn = jen.Id(firstReturnedString)
							} else {
								strReturn = jen.Qual("fmt", "Sprintf").Call(
									jen.Lit(fmt.Sprintf("'%%+v'%s", strings.Repeat(", '%+v'", len(varNames)-1))),
									jen.ListFunc(func(paramGenerator *jen.Group) {
										for _, varName := range varNames {
											paramGenerator.Id(varName)
										}
									}),
								)
							}

							caseGenerator.Return(strReturn, jen.Nil())
						})
				}
				generator.Empty()
				generator.Default().Block(
					jen.Return(jen.Lit(""), jen.Qual("errors", "New").Call(jen.Qual("fmt", "Sprintf").Call(jen.Lit("unknown function '%s'"), jen.Id("functionName")))),
				)
			}),
		)
}

func writeCombinedTreeAndGenerated(tree *dst.File, generated *jen.File, writer io.Writer, genType byte) (int, error) {
	var filePrefix string
	if genType == SERVER {
		// nothing
	} else if genType == CLIENT {
		tree.Name.Name = "main"
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

	inputData := Input{
		FileName:  *inputParameter,
		Functions: make([]FuncInfo, 0),
		TypeMap:   make(map[string]dst.Node),
	}

	inputData.TypeMap = extractTypeMap(tree)
	inputData.Functions = extractFuncInfo(tree, inputData.TypeMap)

	lo.ForEach(inputData.Functions, func(info FuncInfo, _ int) {
		log.Debugf("Function: %s", info)
	})

	newFile := jen.NewFile("main")
	switch generatorType {
	case SERVER:
		modifyOriginalFunctions(tree)
		newFile.Add(generateServerReceiver(inputData.Functions))
	case CLIENT:
		removeOriginalAndUnexportedFunctions(tree)
		newFile.Add(generateJsValueToAny())
		for _, info := range inputData.Functions {
			newFile.Add(generateNewClientFunc(info))
		}
		newFile.Add(generateJSSendMessageFunction())
		newFile.Add(generateClientMain(inputData.Functions))
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
