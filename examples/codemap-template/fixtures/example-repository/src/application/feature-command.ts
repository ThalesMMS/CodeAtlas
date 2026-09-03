export class ValidationError extends Error {}

export interface FeatureCommand {
  id: string;
  value: string;
}

export const FeatureCommand = {
  validate(command: FeatureCommand): void {
    if (!command.id) throw new ValidationError("id is required");
    if (!command.value) throw new ValidationError("value is required");
  },
};
