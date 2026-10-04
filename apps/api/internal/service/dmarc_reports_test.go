package service

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/mail"
	"strings"
	"testing"
)

const dmarcTestRecord = `<record><row><source_ip>192.0.2.1</source_ip><count>1</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>pass</spf></policy_evaluated></row><identifiers><header_from>one.test</header_from></identifiers><auth_results><dkim><domain>one.test</domain><result>pass</result></dkim></auth_results></record>`

func dmarcTestXML(domain string, records int) []byte {
	return []byte(`<?xml version="1.0"?><feedback><report_metadata><org_name>Receiver</org_name><report_id>report-1</report_id><date_range><begin>1700000000</begin><end>1700086400</end></date_range></report_metadata><policy_published><domain>` + domain + `</domain><p>quarantine</p></policy_published>` + strings.Repeat(dmarcTestRecord, records) + `</feedback>`)
}
func dmarcTestGzip(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func dmarcTestZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func dmarcTestParsed(data []byte, name string) *parsedIncoming {
	return &parsedIncoming{Header: mail.Header{"Subject": []string{"Report domain: one.test"}}, Attachments: []AttachmentInfo{{Filename: name, ContentType: "application/octet-stream", Data: data}}}
}
func dmarcTestVerdicts() dmarcVerdicts {
	return dmarcVerdicts{DMARC: "PASS", SPF: "PASS", DKIM: "PASS", Spam: "PASS", Virus: "PASS"}
}

func TestDMARCAggregateFormats(t *testing.T) {
	xml := dmarcTestXML("ONE.TEST.", 1)
	namespaced := bytes.Replace(xml, []byte("<feedback>"), []byte(`<feedback xmlns="urn:ietf:params:xml:ns:dmarc-2.0">`), 1)
	prefixed := []byte(strings.ReplaceAll(strings.ReplaceAll(string(xml), "<feedback>", `<d:feedback xmlns:d="urn:dmarc">`), "</feedback>", "</d:feedback>"))
	for _, tc := range []struct {
		name, filename string
		data           []byte
	}{{"xml", "report.xml", xml}, {"namespace", "report.xml", namespaced}, {"prefix", "report.xml", prefixed}, {"gzip", "report.xml.gz", dmarcTestGzip(t, xml)}, {"zip", "report.zip", dmarcTestZip(t, map[string][]byte{"../report.xml": xml})}} {
		t.Run(tc.name, func(t *testing.T) {
			got, why := dmarcReportDomains(dmarcTestParsed(tc.data, tc.filename), dmarcTestVerdicts())
			if len(got) != 1 || got[0] != "one.test" {
				t.Fatalf("got %v: %s", got, why)
			}
		})
	}
}
func TestDMARCAggregateTrustAndConversations(t *testing.T) {
	for _, tc := range []struct {
		name          string
		verdicts      dmarcVerdicts
		header, value string
		want          bool
	}{
		{name: "dmarc and spf", verdicts: dmarcVerdicts{DMARC: "PASS", SPF: "PASS"}, want: true},
		{name: "dmarc and dkim", verdicts: dmarcVerdicts{DMARC: "PASS", DKIM: "PASS"}, want: true},
		{name: "missing", verdicts: dmarcVerdicts{}, header: "Authentication-Results", value: "mx.example; dmarc=pass; spf=pass"},
		{name: "dmarc fail", verdicts: dmarcVerdicts{DMARC: "FAIL", SPF: "PASS", DKIM: "PASS"}},
		{name: "both other fail", verdicts: dmarcVerdicts{DMARC: "PASS", SPF: "FAIL", DKIM: "FAIL"}},
		{name: "spam", verdicts: dmarcVerdicts{DMARC: "PASS", SPF: "PASS", Spam: "FAIL"}},
		{name: "virus", verdicts: dmarcVerdicts{DMARC: "PASS", DKIM: "PASS", Virus: "FAIL"}},
		{name: "reply", verdicts: dmarcTestVerdicts(), header: "Subject", value: "Re: report"},
		{name: "forward", verdicts: dmarcTestVerdicts(), header: "Subject", value: "Fwd: report"},
		{name: "reference", verdicts: dmarcTestVerdicts(), header: "References", value: "<old@test>"},
		{name: "in reply to", verdicts: dmarcTestVerdicts(), header: "In-Reply-To", value: "<old@test>"},
		{name: "resent", verdicts: dmarcTestVerdicts(), header: "Resent-From", value: "person@example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := dmarcTestParsed(dmarcTestXML("one.test", 1), "report.xml")
			if tc.header != "" {
				p.Header[tc.header] = []string{tc.value}
			}
			got, why := dmarcReportDomains(p, tc.verdicts)
			if (len(got) > 0) != tc.want {
				t.Fatalf("got %v: %s", got, why)
			}
		})
	}
}
func TestDMARCAggregateMalformed(t *testing.T) {
	good := string(dmarcTestXML("one.test", 1))
	for _, tc := range []struct{ name, data string }{
		{"ordinary xml", "<order><domain>one.test</domain></order>"},
		{"empty record", strings.Replace(good, dmarcTestRecord, "<record/>", 1)},
		{"duplicate scalar", strings.Replace(good, "<domain>one.test</domain>", "<domain>one.test</domain><domain>other.test</domain>", 1)},
		{"nested domain", strings.Replace(good, "<domain>one.test</domain>", "<domain><evil/>one.test</domain>", 1)},
		{"nested count", strings.Replace(good, "<count>1</count>", "<count><evil/>1</count>", 1)},
		{"duplicate identities", strings.Replace(good, "</identifiers>", "</identifiers><identifiers/>", 1)},
		{"invalid ip", strings.Replace(good, "192.0.2.1", "not-ip", 1)},
		{"invalid count", strings.Replace(good, "<count>1</count>", "<count>0</count>", 1)},
		{"dtd", "<!DOCTYPE feedback [<!ENTITY x SYSTEM 'file:///etc/passwd'>]>" + good},
		{"entity", strings.Replace(good, "Receiver", "&undefined;", 1)},
		{"bad range", strings.Replace(good, "1700086400", "1699999999", 1)},
		{"missing record", string(dmarcTestXML("one.test", 0))},
		{"too deep", strings.Replace(good, "</feedback>", strings.Repeat("<x>", 64)+strings.Repeat("</x>", 64)+"</feedback>", 1)},
		{"truncated", good[:len(good)-5]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := dmarcReportDomains(dmarcTestParsed([]byte(tc.data), "report.xml"), dmarcTestVerdicts()); len(got) != 0 {
				t.Fatal("invalid aggregate classified")
			}
		})
	}
}
func TestDMARCAggregateBudgets(t *testing.T) {
	good := dmarcTestXML("one.test", 1)
	many := map[string][]byte{}
	for i := 0; i < 33; i++ {
		many[strings.Repeat("x", i+1)+".xml"] = good
	}
	for _, tc := range []struct {
		name, filename string
		data           []byte
	}{
		{"entry limit", "report.zip", dmarcTestZip(t, many)},
		{"nested gzip", "report.zip", dmarcTestZip(t, map[string][]byte{"nested.xml": dmarcTestGzip(t, good)})},
		{"nested name", "report.zip", dmarcTestZip(t, map[string][]byte{"report.gz": good})},
		{"gzip concatenated", "report.gz", append(dmarcTestGzip(t, good), dmarcTestGzip(t, good)...)},
		{"compression bomb", "report.gz", dmarcTestGzip(t, bytes.Repeat([]byte("x"), maxDMARCExpanded+1))},
		{"plain limit", "report.xml", bytes.Repeat([]byte("x"), maxDMARCExpanded+1)},
		{"compressed limit", "report.zip", append([]byte("PKxx"), make([]byte, maxDMARCCompressed)...)},
		{"record limit", "report.xml", dmarcTestXML("one.test", maxDMARCRecords+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := dmarcReportDomains(dmarcTestParsed(tc.data, tc.filename), dmarcTestVerdicts()); len(got) != 0 {
				t.Fatal("budget exceeded but classified")
			}
		})
	}
	manyGzip := dmarcTestParsed(dmarcTestGzip(t, good), "report.gz")
	for i := 0; i < maxDMARCArchiveEntries; i++ {
		manyGzip.Attachments = append(manyGzip.Attachments, manyGzip.Attachments[0])
	}
	if got, _ := dmarcReportDomains(manyGzip, dmarcTestVerdicts()); len(got) > 0 {
		t.Fatal("gzip members did not count toward archive entry limit")
	}
	p := dmarcTestParsed(dmarcTestXML("one.test", 5001), "a.xml")
	p.Attachments = append(p.Attachments, AttachmentInfo{Filename: "b.xml", Data: dmarcTestXML("one.test", 5000)})
	if got, _ := dmarcReportDomains(p, dmarcTestVerdicts()); len(got) > 0 {
		t.Fatal("per-message records budget reset for second attachment")
	}
	p = dmarcTestParsed(dmarcTestXML("one.test", maxDMARCRecords), "report.xml")
	if got, why := dmarcReportDomains(p, dmarcTestVerdicts()); len(got) != 1 {
		t.Fatalf("exact record limit rejected: %s", why)
	}
}
