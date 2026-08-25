-- Enforces meeting-interaction idempotency at the database level, not
-- just in the service layer's check-then-insert: re-importing the same
-- SymMeet meeting id for the same person/organization must never create
-- a second interaction, even under concurrent imports. See
-- internal/service/relationship's ImportPersonMeeting/
-- ImportOrganizationMeeting and docs/integrations/SYMMEET.md.
CREATE UNIQUE INDEX interactions_meeting_person_external_ref_unique
	ON interactions(person_id, external_ref)
	WHERE kind = 'meeting' AND external_ref IS NOT NULL AND person_id IS NOT NULL;
CREATE UNIQUE INDEX interactions_meeting_org_external_ref_unique
	ON interactions(organization_id, external_ref)
	WHERE kind = 'meeting' AND external_ref IS NOT NULL AND organization_id IS NOT NULL;
