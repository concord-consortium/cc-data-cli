# READ-ONLY. Exports one row per learner for the Dataflow activities, including
# the portal_learners secure_key.
#
# This is the same row set report-service uploads to S3 as learner JSON before it
# builds the Athena query (LearnerData.fetch/3 then upload/1). Exporting it lets
# us run the Athena query ourselves -- with `app` constrained, which the report
# server never does -- and join the learner metadata locally instead of joining
# the "report-service"."learners" table.
#
# The secure_key IS the credential-like part of a run_remote_endpoint URL. The
# output belongs in local-data/ and must never be committed.
#
#   sudo -n docker exec -i <container> bundle exec rails runner - < learner_keys.rb

ACTIVITY_IDS = [2460, 2462, 2463, 2464, 2465, 2467, 2468, 3052, 3053,
                2735, 2739, 3255, 3529, 3530, 2841].freeze

sql = <<~SQL
  SELECT DISTINCT
         po.runnable_id AS activity_id,
         rl.learner_id,
         rl.student_id,
         rl.class_id,
         rl.class_name,
         rl.school_name,
         rl.user_id,
         COALESCE(u.primary_account_id, u.id) AS primary_user_id,
         rl.offering_id,
         rl.username,
         rl.last_run,
         ea.url  AS runnable_url,
         pl.secure_key
  FROM report_learners rl
  JOIN portal_learners pl ON (rl.learner_id = pl.id)
  JOIN users u ON (u.id = rl.user_id)
  JOIN portal_offerings po ON (po.id = rl.offering_id)
  JOIN external_activities ea ON (po.runnable_type = 'ExternalActivity' AND po.runnable_id = ea.id)
  JOIN portal_student_clazzes psc ON (psc.student_id = rl.student_id)
  JOIN portal_teacher_clazzes ptc ON (ptc.clazz_id = psc.clazz_id AND rl.class_id = ptc.clazz_id)
  WHERE po.runnable_id IN (#{ACTIVITY_IDS.join(',')})
SQL

require 'csv'
rows = ActiveRecord::Base.connection.select_all(sql).to_a
cols = %w[activity_id learner_id student_id class_id class_name school_name user_id
          primary_user_id offering_id username last_run runnable_url secure_key]
puts CSV.generate_line(cols).chomp
rows.each { |r| puts CSV.generate_line(cols.map { |c| r[c] }).chomp }
warn "rows: #{rows.size}  distinct secure_keys: #{rows.map { |r| r['secure_key'] }.uniq.size}"
