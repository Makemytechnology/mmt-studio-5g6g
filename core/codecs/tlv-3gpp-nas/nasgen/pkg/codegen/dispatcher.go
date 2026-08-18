// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"github.com/dave/jennifer/jen"
)

// generateDispatcher emits DecodeNASMessage(data) (interface{}, error).
//
// First-byte routing:
//
//	0x7E         → 5GMM (plain) or security-protected 5GMM (SHT != 0)
//	0x2E         → 5GSM
//	low nibble 0x07 → LTE EMM (SHT in high nibble; non-zero = security wrapper)
//	low nibble 0x02 → LTE ESM (EBI in high nibble)
//
// Security-protected LTE EMM (SHT != 0) is surfaced as *NASSecurityHeader;
// 5G wrapper same.
func (g *Generator) generateDispatcher(f *jen.File) {
	f.Func().Id("DecodeNASMessage").Params(jen.Id("data").Index().Byte()).
		Params(jen.Interface(), jen.Error()).
		BlockFunc(func(grp *jen.Group) {
			grp.If(jen.Len(jen.Id("data")).Op("<").Lit(2)).Block(
				jen.Return(jen.Nil(), g.qualRuntime("ErrBufferTooShort")),
			)
			grp.Id("b0").Op(":=").Id("data").Index(jen.Lit(0))

			// 5GMM security-protected wrapper
			grp.If(jen.Id("b0").Op("==").Add(g.qualRuntime("EPD5GMM")).
				Op("&&").Id("data").Index(jen.Lit(1)).Op("&").Lit(0x0F).Op("!=").Lit(0)).Block(
				jen.Return(g.qualRuntime("ParseSecurityHeader").Call(jen.Id("data"))),
			)
			// LTE EMM security-protected wrapper (SHT in high nibble of byte 0)
			grp.If(jen.Parens(jen.Id("b0").Op("&").Lit(0x0F)).Op("==").Add(g.qualRuntime("PDEMM")).
				Op("&&").Parens(jen.Id("b0").Op(">>").Lit(4)).Op("!=").Lit(0)).Block(
				jen.Return(g.qualRuntime("ParseSecurityHeader").Call(jen.Id("data"))),
			)

			byEPD := map[uint8][]string{}
			for _, m := range g.Repo.Messages {
				byEPD[m.EPD] = append(byEPD[m.EPD], m.Name)
			}

			emitDispatch := func(grp *jen.Group, epd uint8, names []string) {
				// Each branch needs its own min-length guard because the top
				// guard (2 bytes) is only sufficient for the PD check.
				switch epd {
				case 0x2E:
					grp.If(jen.Len(jen.Id("data")).Op("<").Lit(4)).Block(
						jen.Return(jen.Nil(), g.qualRuntime("ErrBufferTooShort")))
					grp.Id("pduSessID").Op(":=").Id("data").Index(jen.Lit(1))
					grp.Id("pti").Op(":=").Id("data").Index(jen.Lit(2))
					grp.Id("msgType").Op(":=").Id("data").Index(jen.Lit(3))
					grp.Id("payload").Op(":=").Id("data").Index(jen.Lit(4).Op(":"))
					grp.Switch(jen.Id("msgType")).BlockFunc(func(sw *jen.Group) {
						for _, n := range names {
							mt := g.msgTypeOf(n)
							sw.Case(jen.Lit(int(mt))).BlockFunc(func(c *jen.Group) {
								c.Id("msg").Op(":=").Op("&").Id(GoName(n)).Values()
								c.Id("msg").Dot("PDUSessionID").Op("=").Id("pduSessID")
								c.Id("msg").Dot("PTI").Op("=").Id("pti")
								c.Return(jen.Id("msg"), jen.Id("msg").Dot("Decode").Call(jen.Id("payload")))
							})
						}
					})
				case 0x02:
					// LTE ESM: byte 0 = (EBI<<4)|0x02, byte 1 = PTI, byte 2 = msgType, rest = IEs
					grp.If(jen.Len(jen.Id("data")).Op("<").Lit(3)).Block(
						jen.Return(jen.Nil(), g.qualRuntime("ErrBufferTooShort")))
					grp.Id("ebi").Op(":=").Id("data").Index(jen.Lit(0)).Op(">>").Lit(4)
					grp.Id("pti").Op(":=").Id("data").Index(jen.Lit(1))
					grp.Id("msgType").Op(":=").Id("data").Index(jen.Lit(2))
					grp.Id("payload").Op(":=").Id("data").Index(jen.Lit(3).Op(":"))
					grp.Switch(jen.Id("msgType")).BlockFunc(func(sw *jen.Group) {
						for _, n := range names {
							mt := g.msgTypeOf(n)
							sw.Case(jen.Lit(int(mt))).BlockFunc(func(c *jen.Group) {
								c.Id("msg").Op(":=").Op("&").Id(GoName(n)).Values()
								c.Id("msg").Dot("EPSBearerIdentity").Op("=").Id("ebi")
								c.Id("msg").Dot("PTI").Op("=").Id("pti")
								c.Return(jen.Id("msg"), jen.Id("msg").Dot("Decode").Call(jen.Id("payload")))
							})
						}
					})
				case 0x07:
					// LTE EMM plain: byte 0 = 0x07 (SHT=0), byte 1 = msgType.
					grp.If(jen.Len(jen.Id("data")).Op("<").Lit(2)).Block(
						jen.Return(jen.Nil(), g.qualRuntime("ErrBufferTooShort")))
					grp.Id("msgType").Op(":=").Id("data").Index(jen.Lit(1))
					grp.Id("payload").Op(":=").Id("data").Index(jen.Lit(2).Op(":"))
					grp.Switch(jen.Id("msgType")).BlockFunc(func(sw *jen.Group) {
						for _, n := range names {
							mt := g.msgTypeOf(n)
							sw.Case(jen.Lit(int(mt))).BlockFunc(func(c *jen.Group) {
								c.Id("msg").Op(":=").Op("&").Id(GoName(n)).Values()
								c.Return(jen.Id("msg"), jen.Id("msg").Dot("Decode").Call(jen.Id("payload")))
							})
						}
					})
				default:
					// 5GMM: byte 0 = 0x7E, byte 1 = SHT=0, byte 2 = msgType.
					grp.If(jen.Len(jen.Id("data")).Op("<").Lit(3)).Block(
						jen.Return(jen.Nil(), g.qualRuntime("ErrBufferTooShort")))
					grp.Id("msgType").Op(":=").Id("data").Index(jen.Lit(2))
					grp.Id("payload").Op(":=").Id("data").Index(jen.Lit(3).Op(":"))
					grp.Switch(jen.Id("msgType")).BlockFunc(func(sw *jen.Group) {
						for _, n := range names {
							mt := g.msgTypeOf(n)
							sw.Case(jen.Lit(int(mt))).BlockFunc(func(c *jen.Group) {
								c.Id("msg").Op(":=").Op("&").Id(GoName(n)).Values()
								c.Return(jen.Id("msg"), jen.Id("msg").Dot("Decode").Call(jen.Id("payload")))
							})
						}
					})
				}
			}

			// Only emit a dispatch block if there are actually messages loaded
			// for that EPD — avoids "declared but not used" compile errors when
			// a protocol filter excludes an entire family.
			if len(byEPD[0x7E]) > 0 {
				grp.If(jen.Id("b0").Op("==").Lit(0x7E)).BlockFunc(func(c *jen.Group) {
					emitDispatch(c, 0x7E, byEPD[0x7E])
				})
			}
			if len(byEPD[0x2E]) > 0 {
				grp.If(jen.Id("b0").Op("==").Lit(0x2E)).BlockFunc(func(c *jen.Group) {
					emitDispatch(c, 0x2E, byEPD[0x2E])
				})
			}
			if len(byEPD[0x07]) > 0 {
				grp.If(jen.Parens(jen.Id("b0").Op("&").Lit(0x0F)).Op("==").Add(g.qualRuntime("PDEMM"))).BlockFunc(func(c *jen.Group) {
					emitDispatch(c, 0x07, byEPD[0x07])
				})
			}
			if len(byEPD[0x02]) > 0 {
				grp.If(jen.Parens(jen.Id("b0").Op("&").Lit(0x0F)).Op("==").Add(g.qualRuntime("PDESM"))).BlockFunc(func(c *jen.Group) {
					emitDispatch(c, 0x02, byEPD[0x02])
				})
			}
			grp.Return(jen.Nil(), g.qualRuntime("ErrInvalidEPD"))
		})
}

func (g *Generator) msgTypeOf(name string) uint8 {
	for _, mm := range g.Repo.Messages {
		if mm.Name == name {
			return mm.MessageType
		}
	}
	return 0
}
