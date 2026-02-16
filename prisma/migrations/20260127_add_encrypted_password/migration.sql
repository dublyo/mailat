-- Add encrypted_password column to identities table for JMAP authentication
-- This stores the AES-encrypted password that can be decrypted for Stalwart JMAP auth
ALTER TABLE "identities" ADD COLUMN IF NOT EXISTS "encrypted_password" TEXT;
