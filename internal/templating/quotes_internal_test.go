package templating

import (
	"context"
	"errors"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/osteele/liquid"
	"github.com/osteele/liquid/values"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// positions are the places a range can take in a template, with E standing for the expression:
// each pipes what E yields, or in a for loop each item, to the probe filter.
var positions = []string{
	"{{ E | probe }}",
	"{% assign x = E %}{{ x | probe }}",
	"{% for i in E %}{{ i | probe }}{% endfor %}",
	"{% case 1 %}{% when E %}{{ 1 | probe }}{% endcase %}",
}

// probed is what one engine made of a template: whether the probe saw a range (a values.Range,
// or in a for loop an integer item, which only a range yields from this corpus), and whether the
// engine failed.
type probed struct {
	ranged bool
	failed error
}

// probeFilter records in p whether its input is a range, or, in for, an integer item.
func probeFilter(p *probed, item bool) func(any) any {
	return func(v any) any {
		switch v.(type) {
		case values.Range:
			p.ranged = true
		case int, int64:
			p.ranged = p.ranged || item
		}
		return ""
	}
}

var _ = Describe("lola's string literals and liquid's scanner", func() {
	BeforeEach(func() { testenv.Isolate() })

	It(".. inside a quoted string is allowed, and lola's string boundaries match liquid's scanner", func() {
		corpus := []struct {
			expr   string
			ranged bool // lola refuses it as a range
		}{
			{`'\'..'`, true}, {`"\"..\""`, false}, {`'a..b'`, false}, {`"a..b"`, false}, {`"\\"..`, true},
			{`"a'..'b"`, false}, {`'a"..."b'`, false}, {`'a"'..'"b'`, true}, {`"a\"..'"`, false},
			{`'a..`, true}, {`"a..`, true}, {`"a\"..`, true}, {`(1..3)`, true}, {`('a'..'b')`, true}, {`(1..'3')`, true},
		}
		for _, c := range corpus {
			for _, pos := range positions {
				body := strings.ReplaceAll(pos, "E", c.expr)
				std := probed{}
				eng := liquid.NewEngine()
				eng.RegisterFilter("probe", probeFilter(&std, strings.HasPrefix(pos, "{% for")))
				func() {
					defer func() {
						if r := recover(); r != nil {
							std.failed = fmt.Errorf("panic: %v", r)
						}
					}()
					_, std.failed = eng.ParseAndRenderString(body, map[string]any{})
				}()

				mine := probed{}
				extra := map[string]any{"probe": probeFilter(&mine, strings.HasPrefix(pos, "{% for"))}
				_, err := renderWith(context.Background(), Source{File: "t.md.liquid", Line: 1, Body: body}, Partials{}, Data{}, DefaultLimits(), nil, nil, extra)
				var e *Error
				refused := errors.As(err, &e) && e.Code == codeRangeUnavailable

				Expect(refused).To(Equal(c.ranged), "%s: lola's refusal", body)
				Expect(mine.ranged).To(BeFalse(), "%s: lola rendered a range", body)
				if std.ranged {
					Expect(refused).To(BeTrue(), "%s: liquid yields a range that lola accepts", body)
				}
				if std.failed != nil {
					Expect(err).To(HaveOccurred(), "%s: liquid fails (%v) but lola renders", body, std.failed)
				}
				if !c.ranged {
					Expect(std.ranged).To(BeFalse(), "%s: lola reads a string where liquid yields a range", body)
				}
			}
			ranged := false
			for _, pos := range positions[:3] {
				std := probed{}
				eng := liquid.NewEngine()
				eng.RegisterFilter("probe", probeFilter(&std, strings.HasPrefix(pos, "{% for")))
				_, std.failed = eng.ParseAndRenderString(strings.ReplaceAll(pos, "E", c.expr), map[string]any{})
				ranged = ranged || std.ranged || std.failed != nil
			}
			Expect(ranged).To(Equal(c.ranged), "%s: liquid yields a range or fails exactly where lola refuses", c.expr)
		}
	})
})
