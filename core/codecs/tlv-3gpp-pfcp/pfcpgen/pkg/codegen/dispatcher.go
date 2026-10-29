// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"github.com/dave/jennifer/jen"
)

// generateDispatcher emits DecodePFCPMessage(data []byte) (interface{}, error).
//
// It parses the header, picks the concrete struct by MessageType, copies the
// header-level fields (SEID, SequenceNumber, Priority) onto it, and calls
// msg.Decode(payload) where payload is the IE section of the PDU.
func (g *Generator) generateDispatcher(f *jen.File) {
	f.Func().Id("DecodePFCPMessage").Params(jen.Id("data").Index().Byte()).
		Params(jen.Interface(), jen.Error()).
		BlockFunc(func(grp *jen.Group) {
			grp.List(jen.Id("h"), jen.Id("off"), jen.Id("err")).Op(":=").
				Add(g.qualRuntime("ParseHeader")).Call(jen.Id("data"))
			grp.If(jen.Id("err").Op("!=").Nil()).Block(jen.Return(jen.Nil(), jen.Id("err")))
			grp.Id("payload").Op(":=").Id("data").Index(jen.Id("off").Op(":"))
			grp.Switch(jen.Id("h").Dot("MessageType")).BlockFunc(func(sw *jen.Group) {
				for _, m := range g.Repo.Messages {
					mm := m
					sw.Case(jen.Lit(int(mm.MessageType))).BlockFunc(func(cs *jen.Group) {
						cs.Id("msg").Op(":=").Op("&").Id(GoName(mm.Name)).Values()
						cs.Id("msg").Dot("SequenceNumber").Op("=").Id("h").Dot("SequenceNumber")
						cs.Id("msg").Dot("Priority").Op("=").Id("h").Dot("Priority")
						if mm.HasSEID {
							cs.Id("msg").Dot("SEID").Op("=").Id("h").Dot("SEID")
						}
						cs.Return(jen.Id("msg"), jen.Id("msg").Dot("Decode").Call(jen.Id("payload")))
					})
				}
			})
			grp.Return(jen.Nil(), g.qualRuntime("ErrInvalidMessageType"))
		})
}
