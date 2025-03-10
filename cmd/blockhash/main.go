package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log"
	"math/bits"
	"os"
	"sort"
	"strconv"
)

func main() {
	out := flag.String("o", "", "output file for hash constants and methods")
	flag.Parse()

	if len(flag.Args()) != 1 {
		log.Fatalln("Must pass one package to produce block hashes for.")
	}
	fs := token.NewFileSet()
	packages, err := parser.ParseDir(fs, flag.Args()[0], nil, parser.ParseComments)
	if err != nil {
		log.Fatalln(err)
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalln(err)
	}
	for _, pkg := range packages {
		procPackage(pkg, fs, f)
	}
	_ = f.Close()
}

func procPackage(pkg *ast.Package, fs *token.FileSet, w io.Writer) {
	b := &hashBuilder{
		fs:          fs,
		pkg:         pkg,
		fields:      make(map[string][]*ast.Field),
		aliases:     make(map[string]string),
		handled:     map[string]struct{}{},
		funcs:       map[string]*ast.FuncDecl{},
		blockFields: map[string][]*ast.Field{},
	}
	b.readStructFields(pkg)
	b.readFuncs(pkg)
	b.resolveBlocks()
	b.sortNames()

	b.writePackage(w)
	i := b.writeConstants(w)
	b.writeMethods(w, i)
}

var (
	packageFormat = "// Code generated by cmd/blockhash; DO NOT EDIT.\n\npackage %v\n\n"
	methodFormat  = "\nfunc (%v%v) Hash() uint64 {\n\treturn %v\n}\n"
	constFormat   = "\thash%v"
)

type hashBuilder struct {
	fs          *token.FileSet
	pkg         *ast.Package
	fields      map[string][]*ast.Field
	funcs       map[string]*ast.FuncDecl
	aliases     map[string]string
	handled     map[string]struct{}
	blockFields map[string][]*ast.Field
	names       []string
}

// sortNames sorts the names of the blockFields map and stores them in a slice.
func (b *hashBuilder) sortNames() {
	b.names = make([]string, 0, len(b.blockFields))
	for name := range b.blockFields {
		b.names = append(b.names, name)
	}
	sort.Slice(b.names, func(i, j int) bool {
		return b.names[i] < b.names[j]
	})
}

// writePackage writes the package at the top of the file.
func (b *hashBuilder) writePackage(w io.Writer) {
	if _, err := fmt.Fprintf(w, packageFormat, b.pkg.Name); err != nil {
		log.Fatalln(err)
	}
}

// writeConstants writes hash constants for every block to a file.
func (b *hashBuilder) writeConstants(w io.Writer) (bitSize int) {
	if _, err := fmt.Fprintln(w, "const ("); err != nil {
		log.Fatalln(err)
	}

	var i uint64
	for _, name := range b.names {
		c := constFormat
		if i == 0 {
			c += " = iota"
		}

		if _, err := fmt.Fprintf(w, c+"\n", name); err != nil {
			log.Fatalln(err)
		}
		i++
	}

	if _, err := fmt.Fprintln(w, ")"); err != nil {
		log.Fatalln(err)
	}

	return bits.Len64(i)
}

func (b *hashBuilder) writeMethods(w io.Writer, baseBits int) {
	for _, name := range b.names {
		fields := b.blockFields[name]

		h := "hash" + name
		bitSize := baseBits

		fun := b.funcs[name]
		var recvName string
		for _, n := range fun.Recv.List[0].Names {
			recvName = n.Name
		}
		pos := b.fs.Position(fun.Body.Pos())
		f, err := os.Open(pos.Filename)
		if err != nil {
			log.Fatalln(err)
		}
		body := make([]byte, fun.Body.End()-fun.Body.Pos())

		if _, err := f.ReadAt(body, int64(pos.Offset)); err != nil {
			log.Fatalln(err)
		}
		_ = f.Close()

		for _, field := range fields {
			for _, fieldName := range field.Names {
				if !bytes.Contains(body, []byte(fieldName.Name)) {
					// Field was not used in the EncodeBlock method, so we can assume it's not a property and thus
					// should not be in the Hash method.
					continue
				}
				if !fieldName.IsExported() {
					continue
				}
				str, v := b.ftype(name, recvName+"."+fieldName.Name, field.Type)
				if v == 0 {
					// Assume this field is not used in the hash.
					continue
				}

				if bitSize > 64 {
					log.Println("Hash size of block properties of", name, "exceeds", 64-baseBits, "bits. Please look at this manually.")
				} else {
					h += " | " + str + "<<" + strconv.Itoa(bitSize)
				}
				bitSize += v
			}
		}
		if bitSize == baseBits {
			// No need to have a receiver name if we don't use any of the fields of the block.
			recvName = ""
		}

		if recvName != "" {
			recvName += " "
		}

		if _, err := fmt.Fprintf(w, methodFormat, recvName, name, h); err != nil {
			log.Fatalln(err)
		}
	}
	log.Println("Assuming int size of 8 bits at most for all int fields: Make sure this is valid for all blocks.")
}

func (b *hashBuilder) ftype(structName, s string, expr ast.Expr) (string, int) {
	var name string
	switch t := expr.(type) {
	case *ast.BasicLit:
		name = t.Value
	case *ast.Ident:
		name = t.Name
	case *ast.SelectorExpr:
		name = t.Sel.Name
	default:
		log.Fatalf("unknown field type %#v\n", expr)
		return "", 0
	}
	switch name {
	case "bool":
		return "uint64(boolByte(" + s + "))", 1
	case "int":
		return "uint64(" + s + ")", 8
	case "Attachment":
		return "uint64(" + s + ".Uint8())", 5
	case "FlowerType", "DoubleFlowerType", "Colour":
		// Assuming these were all based on metadata, it should be safe to assume a bit size of 4 for this.
		return "uint64(" + s + ".Uint8())", 4
	case "WoodType", "CoralType":
		return "uint64(" + s + ".Uint8())", 3
	case "SandstoneType", "PrismarineType", "StoneBricksType":
		return "uint64(" + s + ".Uint8())", 2
	case "OreType", "FireType", "GrassType":
		return "uint64(" + s + ".Uint8())", 1
	case "Direction", "Axis":
		return "uint64(" + s + ")", 2
	case "Face":
		return "uint64(" + s + ")", 3
	default:
		log.Println("Found unhandled field type", "'"+name+"'", "in block", structName+".", "Assuming this field is not included in block states. Please make sure this is correct or add the type to cmd/blockhash.")
	}
	return "", 0
}

func (b *hashBuilder) resolveBlocks() {
	for bl, fields := range b.fields {
		if _, ok := b.funcs[bl]; ok {
			b.blockFields[bl] = fields
		}
	}
}

func (b *hashBuilder) readFuncs(pkg *ast.Package) {
	for _, f := range pkg.Files {
		ast.Inspect(f, b.readFuncDecls)
	}
}

func (b *hashBuilder) readFuncDecls(node ast.Node) bool {
	if fun, ok := node.(*ast.FuncDecl); ok {
		// If the function is called 'EncodeBlock' and the receiver is not nil, meaning the function is a method, this
		// is an implementation of the world.Block interface.
		if fun.Name.Name == "EncodeBlock" && fun.Recv != nil {
			b.funcs[fun.Recv.List[0].Type.(*ast.Ident).Name] = fun
		}
	}
	return true
}

func (b *hashBuilder) readStructFields(pkg *ast.Package) {
	for _, f := range pkg.Files {
		ast.Inspect(f, b.readStructs)
	}
	b.resolveEmbedded()
	b.resolveAliases()
}

func (b *hashBuilder) resolveAliases() {
	for name, alias := range b.aliases {
		b.fields[name] = b.findFields(alias)
	}
}

func (b *hashBuilder) findFields(structName string) []*ast.Field {
	for {
		if fields, ok := b.fields[structName]; ok {
			// Alias found in the fields map, so it referred to a struct directly.
			return fields
		}
		if nested, ok := b.aliases[structName]; ok {
			// The alias itself was an alias, so continue with the next.
			structName = nested
			continue
		}
		// Neither an alias nor a struct: Break as this isn't going to go anywhere.
		return nil
	}
}

func (b *hashBuilder) resolveEmbedded() {
	for name, fields := range b.fields {
		if _, ok := b.handled[name]; ok {
			// Don't handle if a previous run already handled this struct.
			continue
		}
		newFields := make([]*ast.Field, 0, len(fields))
		for _, f := range fields {
			if len(f.Names) == 0 {
				// We're dealing with an embedded struct here. They're of the type ast.Ident.
				if ident, ok := f.Type.(*ast.Ident); ok {
					for _, af := range b.findFields(ident.Name) {
						if len(af.Names) == 0 {
							// The struct this referred is embedding a struct itself which hasn't yet been processed,
							// so we need to rerun and hope that struct is handled next. This isn't a very elegant way,
							// and could lead to a lot of runs, but in general it's fast enough and does the job.
							b.resolveEmbedded()
							return
						}
					}
					newFields = append(newFields, b.findFields(ident.Name)...)
				}
			} else {
				newFields = append(newFields, f)
			}
		}
		// Make sure a next run doesn't end up handling this struct again.
		b.handled[name] = struct{}{}
		b.fields[name] = newFields
	}
}

func (b *hashBuilder) readStructs(node ast.Node) bool {
	if s, ok := node.(*ast.TypeSpec); ok {
		switch t := s.Type.(type) {
		case *ast.StructType:
			b.fields[s.Name.Name] = t.Fields.List
		case *ast.Ident:
			// This is a type created something like 'type Andesite polishable': A type alias. We need to handle
			// these later, first parse all struct types.
			b.aliases[s.Name.Name] = t.Name
		}
	}
	return true
}
