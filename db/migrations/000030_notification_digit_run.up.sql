-- 000030: the digit run that means "identity number" is ten, not eight.
--
-- Migration 000029 refused any run of eight digits in a template body, a subject or a
-- rendered message's safe variables, on the stated grounds that "nothing a notification
-- legitimately carries has eight of them in a row". That premise is wrong, and the thing
-- it is most wrong about is the one value a notification exists to carry: a service
-- request reference is minted as `SR-YYYYMMDD-XXXXXXXX` (internal/servicerequest/
-- application/service.go), and the date in the middle is exactly eight digits. A member
-- could not have been told their own request number.
--
-- The rule's purpose is identity numbers, and the shortest of those is a ten-digit VKN; a
-- TCKN has eleven. Ten catches both and lets a date through. It also raises the accidental
-- ceiling the old rule put on an amount, which could not exceed 9.999.999,99 without
-- tripping a check meant for identity numbers.
--
-- This is not a relaxation of what a notification may carry. What it may carry is the
-- closed variable catalogue in internal/notification/domain, which this constraint has
-- never been the real guarantee of — it is the crude second line, and a crude line drawn
-- in the wrong place refuses honest work while catching nothing extra.

ALTER TABLE notification.template
    DROP CONSTRAINT ck_notification_template_no_identity_number,
    ADD CONSTRAINT ck_notification_template_no_identity_number
        CHECK (body !~ '[0-9]{10}' AND (subject IS NULL OR subject !~ '[0-9]{10}'));

ALTER TABLE notification.message
    DROP CONSTRAINT ck_notification_message_variables_no_identity_number,
    ADD CONSTRAINT ck_notification_message_variables_no_identity_number
        CHECK ((safe_variables - 'deep_link')::text !~ '[0-9]{10}');
