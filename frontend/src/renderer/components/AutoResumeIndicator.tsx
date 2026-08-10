import { TimerReset } from "lucide-react";
import { useTranslation } from "react-i18next";
import { usePendingAutoResume } from "../hooks/usePendingAutoResumes";
import { formatClockTime } from "../lib/format-time";
import { cn } from "../lib/utils";

/**
 * U12-6: "this session is not dead, it is waiting for a provider limit to
 * lift." Without it an auto-resumable session is indistinguishable from one
 * that simply exited, and an operator would relaunch it by hand.
 *
 * It renders nothing when the session has no scheduled resume, so it can sit
 * unconditionally in a card or a titlebar.
 */
export function AutoResumeIndicator({ sessionId, className }: { sessionId: string; className?: string }) {
	const { t } = useTranslation();
	const pending = usePendingAutoResume(sessionId);
	if (!pending) return null;

	const time = formatClockTime(pending.resumeAt);
	return (
		<span
			className={cn("flex min-w-0 items-center gap-1.5 font-mono text-2xs text-warning", className)}
			// Whether the time was read off the notice or guessed changes what an
			// operator should do about a resume that fires at an odd hour, so the
			// two never render as the same claim.
			title={
				pending.exactReset
					? t("autoResume.waitingTitle", { attempt: pending.attempt, time })
					: t("autoResume.waitingEstimateTitle", { attempt: pending.attempt, time })
			}
		>
			<TimerReset aria-hidden="true" className="size-icon-2xs shrink-0" />
			<span className="truncate">
				{pending.exactReset ? t("autoResume.waiting", { time }) : t("autoResume.waitingEstimate", { time })}
			</span>
		</span>
	);
}
