package contact

import "testing"

func TestControlledVocabulariesValidateKnownValues(t *testing.T) {
	for _, classification := range []Classification{
		ClassificationPersonal,
		ClassificationBusiness,
		ClassificationCustomer,
		ClassificationLead,
		ClassificationPartner,
	} {
		if !classification.Valid() {
			t.Errorf("Classification(%q).Valid() = false, want true", classification)
		}
	}
	if Classification("unknown").Valid() {
		t.Error("unknown classification is valid, want false")
	}

	for _, kind := range []ContactPointKind{
		ContactPointEmail,
		ContactPointPhone,
		ContactPointAddress,
		ContactPointURL,
		ContactPointHandle,
	} {
		if !kind.Valid() {
			t.Errorf("ContactPointKind(%q).Valid() = false, want true", kind)
		}
	}
	if ContactPointKind("unknown").Valid() {
		t.Error("unknown contact point kind is valid, want false")
	}
}
