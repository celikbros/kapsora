import type { ExportStatus } from '@kapsora/api-client';
import type { BadgeTone } from '@kapsora/ui';

export function runTone(status: string): BadgeTone {
  switch (status) {
    case 'BALANCED':
      return 'success';
    case 'DIFFERENCES':
      return 'warning';
    case 'FAILED':
      return 'danger';
    default:
      return 'neutral';
  }
}

export function exportTone(status: ExportStatus): BadgeTone {
  switch (status) {
    case 'READY':
      return 'success';
    case 'QUEUED':
    case 'RUNNING':
      return 'info';
    case 'FAILED':
      return 'danger';
    case 'EXPIRED':
      return 'neutral';
    default:
      return 'neutral';
  }
}
