import { ChevronRight, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../lib/utils";

function SettingsRowLabel({ icon: Icon, label }: { icon?: LucideIcon; label: string }) {
	return (
		<div className="flex shrink-0 items-center gap-(--size-settings-row-icon-gap)">
			{Icon ? <Icon className="size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" /> : null}
			<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{label}</span>
		</div>
	);
}

/** Settings row bar: tokenized height, radius, padding, and icon gap. */
export function SettingsRow({
	icon,
	label,
	children,
	className,
}: {
	icon?: LucideIcon;
	label: string;
	children: ReactNode;
	className?: string;
}) {
	return (
		<div className={cn("settings-row-bar", className)}>
			<SettingsRowLabel icon={icon} label={label} />
			<div className="flex min-w-0 flex-1 items-center justify-end">{children}</div>
		</div>
	);
}

/**
 * A settings row whose label carries a second line — an endpoint, a checkout
 * path, a drift summary. It is the SAME bar as SettingsRow (same background,
 * radius, padding, icon gap and type scale) grown to fit, which is how
 * UpdatesSection already renders its taller rows. Deliberately not a bordered
 * card: a card inside a list of bars reads as a different component family and
 * makes the newer panes look bolted on.
 */
export function SettingsDetailRow({
	icon: Icon,
	title,
	meta,
	actions,
	className,
}: {
	icon?: LucideIcon;
	title: ReactNode;
	meta?: ReactNode;
	actions?: ReactNode;
	className?: string;
}) {
	return (
		<div className={cn("settings-row-bar h-auto min-h-(--size-settings-row) items-start", className)}>
			<div className="flex min-w-0 flex-1 items-start gap-(--size-settings-row-icon-gap)">
				{Icon ? <Icon className="mt-0.5 size-icon-lg shrink-0 text-settings-muted" aria-hidden="true" /> : null}
				<div className="flex min-w-0 flex-col gap-0.5">
					<div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-sm leading-5 text-settings-label">
						{title}
					</div>
					{meta ? <div className="min-w-0 text-xs leading-4 text-settings-muted">{meta}</div> : null}
				</div>
			</div>
			{actions ? <div className="flex shrink-0 items-center gap-2.5">{actions}</div> : null}
		</div>
	);
}

export function SettingsLinkRow({ icon, label, onClick }: { icon?: LucideIcon; label: string; onClick: () => void }) {
	return (
		<button
			type="button"
			onClick={onClick}
			className="settings-row-bar w-full text-left transition-colors hover:bg-settings-menu-selected"
		>
			<SettingsRowLabel icon={icon} label={label} />
			<ChevronRight className="size-icon-base shrink-0 text-settings-muted" aria-hidden="true" />
		</button>
	);
}
