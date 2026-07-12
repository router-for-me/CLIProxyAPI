import SwiftUI

@MainActor
@Observable
final class ModelsSettingsStore {
    private(set) var selectedModels: Set<String> = []
    private(set) var isSaving = false

    func updateSelection(from exposed: [String]) {
        selectedModels = Set(exposed)
    }

    func toggle(_ modelID: String) {
        if selectedModels.contains(modelID) {
            selectedModels.remove(modelID)
        } else {
            selectedModels.insert(modelID)
        }
    }

    func save(baseURL: URL, secret: String, apiClient: APIClient) async {
        isSaving = true
        await apiClient.setExposedModels(baseURL: baseURL, secret: secret, models: Array(selectedModels).sorted())
        isSaving = false
    }
}

struct ModelsSettingsPane: View {
    @Bindable var settings: SettingsStore
    @Bindable var apiClient: APIClient
    @State private var localStore = ModelsSettingsStore()

    var body: some View {
        ScrollView(.vertical, showsIndicators: true) {
            VStack(alignment: .leading, spacing: 16) {
                HStack(spacing: 12) {
                    Button {
                        Task {
                            await apiClient.extractAuth(baseURL: settings.baseURL, secret: settings.managementSecret)
                            localStore.updateSelection(from: apiClient.exposedModels)
                        }
                    } label: {
                        Label("Detect Subscriptions", systemImage: "magnifyingglass")
                    }

                    Button {
                        Task {
                            await apiClient.fetchSubscriptions(baseURL: settings.baseURL, secret: settings.managementSecret)
                            await apiClient.fetchExposedModels(baseURL: settings.baseURL, secret: settings.managementSecret)
                            localStore.updateSelection(from: apiClient.exposedModels)
                        }
                    } label: {
                        Label("Refresh", systemImage: "arrow.clockwise")
                    }

                    Spacer()

                    if localStore.isSaving || apiClient.isLoading {
                        ProgressView()
                            .controlSize(.small)
                    }

                    if let error = apiClient.lastError {
                        Text(error)
                            .font(.caption)
                            .foregroundStyle(.red)
                            .lineLimit(1)
                    }
                }

                if apiClient.subscriptions.isEmpty {
                    Text("No subscriptions detected yet. Click Detect Subscriptions to scan local auth files.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .padding(.top, 20)
                } else {
                    ForEach(apiClient.subscriptions) { subscription in
                        SubscriptionModelsSection(
                            subscription: subscription,
                            selectedModels: localStore.selectedModels,
                            onToggle: { id in
                                localStore.toggle(id)
                                Task {
                                    await localStore.save(baseURL: settings.baseURL, secret: settings.managementSecret, apiClient: apiClient)
                                }
                            }
                        )
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, 4)
        }
        .task {
            await apiClient.fetchSubscriptions(baseURL: settings.baseURL, secret: settings.managementSecret)
            await apiClient.fetchExposedModels(baseURL: settings.baseURL, secret: settings.managementSecret)
            localStore.updateSelection(from: apiClient.exposedModels)
        }
        .onChange(of: apiClient.exposedModels) { _, newValue in
            localStore.updateSelection(from: newValue)
        }
    }
}

struct SubscriptionModelsSection: View {
    let subscription: Subscription
    let selectedModels: Set<String>
    let onToggle: (String) -> Void

    var body: some View {
        SettingsSection(contentSpacing: 8) {
            HStack {
                Text(subscription.provider.uppercased())
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Text("\(subscription.models.count) models")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }

            ForEach(subscription.models) { model in
                HStack(spacing: 8) {
                    Toggle("", isOn: Binding(
                        get: { selectedModels.contains(model.id) },
                        set: { _ in onToggle(model.id) }
                    ))
                    .toggleStyle(.checkbox)
                    .labelsHidden()

                    VStack(alignment: .leading, spacing: 1) {
                        Text(model.displayName ?? model.id)
                            .font(.body)
                        Text(model.id)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }

                    Spacer()
                }
            }
        }
    }
}
