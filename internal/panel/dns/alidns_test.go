package dns

import "testing"

// The worked example in Alibaba Cloud DNS's signature documentation.
func TestAliSignDocExample(t *testing.T) {
	p := map[string]string{"AccessKeyId": "testid", "Action": "DescribeDomainRecords", "DomainName": "example.com", "Format": "XML",
		"SignatureMethod": "HMAC-SHA1", "SignatureNonce": "f59ed6a9-83fc-473b-9cc6-99c95df3856e", "SignatureVersion": "1.0",
		"Timestamp": "2016-03-24T16:41:54Z", "Version": "2015-01-09"}
	if got := AliSign(p, "testsecret"); got != "uRpHwaSEt3J+6KQD//svCh/x+pI=" {
		t.Fatal(got)
	}
}
