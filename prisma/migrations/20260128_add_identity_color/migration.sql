-- Add color column to identities for UI display
ALTER TABLE "identities" ADD COLUMN IF NOT EXISTS "color" VARCHAR(7);

-- Add index for better performance when querying by domain
CREATE INDEX IF NOT EXISTS "identities_user_id_idx" ON "identities"("user_id");
